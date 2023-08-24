package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/net/webdav"

	_ "embed"
)

//go:embed index.html
var indexpage []byte

//go:embed blank.ass
var blanksubs []byte

var blankimg = []byte{0x89, 0x50, 0x4e, 0x47, 0xd,
	0xa, 0x1a, 0xa, 0x0, 0x0, 0x0, 0xd, 0x49, 0x48,
	0x44, 0x52, 0x0, 0x0, 0x0, 0x1, 0x0, 0x0, 0x0,
	0x1, 0x1, 0x0, 0x0, 0x0, 0x0, 0x37, 0x6e, 0xf9,
	0x24, 0x0, 0x0, 0x0, 0xa, 0x49, 0x44, 0x41, 0x54,
	0x78, 0x1, 0x63, 0x68, 0x0, 0x0, 0x0, 0x82, 0x0,
	0x81, 0x4c, 0x17, 0xd7, 0xdf, 0x0, 0x0, 0x0, 0x0,
	0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82}

var (
	DataDirectory string
	FfmpegBinary  string
	davHandler    *webdav.Handler
	db            *bolt.DB
	thumbbucket   = []byte("thumbs")
	thumblock     sync.Mutex
)

func index(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		w.Header().Set("content-type", "text/html")
		w.Write(indexpage)
		return
	}
	if r.Method == http.MethodGet {
		ex := path.Ext(r.URL.Path)
		if strings.HasPrefix(mime.TypeByExtension(ex), "video/") {
			if r.FormValue("thumb") != "" {
				thumbnailHandler(filepath.Join(DataDirectory, r.URL.Path), w, r)
				return
			}
			if r.FormValue("encode") != "" {
				EncodeVideo(filepath.Join(DataDirectory, r.URL.Path), w, r)
				return
			}
		}
	}

	davHandler.ServeHTTP(w, r)
}

func thumbnailHandler(filename string, w http.ResponseWriter, r *http.Request) {
	if err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(thumbbucket)
		w.Header().Set("Content-Type", "image/png")
		if b == nil {
			w.Write(blankimg)
			return nil
		}
		thumb := b.Get([]byte(filename))
		if thumb != nil {
			// Cache good thumbnails
			w.Header().Set("Cache-Control", "public, max-age=604800")
			w.Write(thumb)
		} else {
			go ExtractFrame(filename)
			w.Write(blankimg)
		}
		return nil
	}); err != nil {
		log.Println(err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

func ExtractFrame(filename string) {
	thumblock.Lock()
	defer thumblock.Unlock()
	var buf bytes.Buffer
	// ffmpeg -ss 00:00:04 -i input.mp4 -frames:v 1 screenshot.png
	framecmd := exec.CommandContext(context.Background(),
		FfmpegBinary,
		// "-ss", "50",
		"-i", filename,
		"-loglevel", "error",
		"-vf", "fps=1,thumbnail=n=30,scale=w=200:h=100",
		"-frames:v", "1",
		"-c", "png",
		"-f", "image2",
		"-update", "1",
		"pipe:1")
	framecmd.Stdout = &buf
	framecmd.Stderr = os.Stderr
	framecmd.Run()

	if err := db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(thumbbucket)
		if err != nil {
			return err
		}
		if err := b.Put([]byte(filename), buf.Bytes()); err != nil {
			return err
		}
		return nil
	}); err != nil {
		log.Println(err)
	}
}

func ExtractSubsOrBlank(filename, sub_index string, destination *os.File) {
	if _, err := strconv.Atoi(sub_index); err != nil {
		log.Println("Bad sub index passed.")
		destination.Write(blanksubs)
	}
	subcmd := exec.CommandContext(context.Background(),
		FfmpegBinary, "-y",
		"-i", filename,
		"-loglevel", "error",
		"-map", fmt.Sprintf("0:s:%s", sub_index),
		"-f", "ass",
		destination.Name())
	subcmd.Stderr = os.Stderr
	if err := subcmd.Run(); err != nil {
		log.Println("No subtitles found, writing empty subs.")
		destination.Write(blanksubs)
	}
}

func EncodeVideo(filename string, w http.ResponseWriter, r *http.Request) {
	if filename == "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	if _, err := filepath.Rel(DataDirectory, filename); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	if mode, err := os.Lstat(filename); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	} else {
		if !mode.Mode().IsRegular() {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
	}

	extsub, err := os.CreateTemp("", "sub*")
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	defer extsub.Close()
	defer os.Remove(extsub.Name())

	sub_index := r.FormValue("si")
	if sub_index == "" {
		sub_index = "0"
	}

	if _, err := strconv.Atoi(sub_index); err != nil {
		log.Println("Bad sub index passed.")
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	// Has local SRT file?
	subname := filename
	if ex := path.Ext(filename); ex != "" {
		srtp := fmt.Sprintf("%s.srt", strings.TrimSuffix(filename, path.Ext(filename)))
		if fi, err := os.Stat(srtp); err == nil && !fi.IsDir() {
			subname = srtp
		} else {
			// Pull the subs from the media file, or make blank ones so -vf subtitles doesnt freak out
			ExtractSubsOrBlank(filename, sub_index, extsub)
			subname = extsub.Name()
		}
	}
	subname = strings.ReplaceAll(subname, "'", "\\'")
	subname = strings.ReplaceAll(subname, "[", "\\[")
	subname = strings.ReplaceAll(subname, "]", "\\]")
	subname = strings.ReplaceAll(subname, ":", "\\:")

	runcmd := []string{
		"-i", filename,
		"-loglevel", "error",
		"-crf", "35",
		"-c:a", "libopus",
		"-af", "aformat=channel_layouts='7.1|5.1|stereo'",
		"-c:v", "libvpx-vp9",
		"-deadline", "realtime",
		"-cpu-used", "8",
		"-row-mt", "1",
		"-f", "webm",
		"-filter:v", fmt.Sprintf("subtitles=filename='%s':force_style='PrimaryColour=&H00d5ff'", subname),
		"pipe:1",
	}

	w.Header().Set("Content-Type", "video/webm")
	w.Header().Set("Content-Disposition", "inline")
	w.Header()["Content-Length"] = nil // Stop go helpfully setting it to 0
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	log.Println(FfmpegBinary, runcmd)

	cmd := exec.CommandContext(r.Context(), FfmpegBinary, runcmd...)
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Println(FfmpegBinary, runcmd)
		log.Println(err)
	}
}

func logRequest(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s %s\n", r.RemoteAddr, r.Method, r.URL)
		handler.ServeHTTP(w, r)
	})
}

func main() {
	flag.StringVar(&DataDirectory, "data", ".", "Data location.")
	flag.StringVar(&FfmpegBinary, "ffmpeg", "ffmpeg", "Ffmpeg invocation command.")
	listen := flag.String("listen", ":8080", "Webserver listen address.")
	flag.Parse()

	if bb, err := bolt.Open(filepath.Join(DataDirectory, "thumbnails.db"), 0600, nil); err == nil {
		bb.Update(func(tx *bolt.Tx) error {
			_, err := tx.CreateBucketIfNotExists(thumbbucket)
			return err
		})
		db = bb
	} else {
		log.Fatalf("Can't open thumbnail db: %q", err)
	}
	defer db.Close()

	davHandler = &webdav.Handler{
		FileSystem: webdav.Dir(DataDirectory),
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil {
				log.Printf("WEBDAV [%s]: %s, ERROR: %s\n", r.Method, r.URL, err)
			}
		},
	}

	http.HandleFunc("/", index)
	log.Fatal(http.ListenAndServe(*listen, logRequest(http.DefaultServeMux)))
}
