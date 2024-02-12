package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
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
	thumblock     sync.Mutex
	tclient       *torrent.Client
	tlock         sync.Mutex
)

type TorrentConfig struct {
	Magnet string `json:"magnet"`
	Name   string `json:"name"`
	Missing int64 `json:"missing"`
}

func index(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		w.Header().Set("content-type", "text/html")
		w.Write(indexpage)
		return
	}

	if r.URL.Path == "/magnets.json" {
		w.Header().Set("content-type", "application/json")
		ManageConfig(w, r)
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
	sum := sha1.Sum([]byte(filename))
	path := filepath.Join(os.TempDir(),
		fmt.Sprintf("thumb%s.png", hex.EncodeToString(sum[:])),
	)

	w.Header().Set("Content-Type", "image/png")

	f, err := os.Open(path)
	if err != nil {
		w.Write(blankimg)
		go ExtractFrame(filename, path)
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", "public, max-age=604800")
	io.Copy(w, f)
}

func ExtractFrame(filename, dest string) {
	thumblock.Lock()
	defer thumblock.Unlock()
	// ffmpeg -ss 00:00:04 -i input.mp4 -frames:v 1 screenshot.png
	framecmd := exec.CommandContext(context.Background(),
		FfmpegBinary,
		"-n",
		"-i", filename,
		"-loglevel", "error",
		"-vf", "fps=1,thumbnail=n=30,scale=w=200:h=100",
		"-frames:v", "1",
		"-c", "png",
		"-f", "image2",
		"-update", "1",
		dest)
	framecmd.Stderr = os.Stderr
	if err := framecmd.Run(); err != nil {
		return
	}
}

func ExtractSubsOrBlank(filename, sub_index string, destination *os.File) {
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

	if ! strings.HasPrefix(sub_index, "m:language:") {
		if _, err := strconv.Atoi(sub_index); err != nil {
			log.Println("Bad sub index passed.")
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
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

	audio_index := r.FormValue("ai")
	if audio_index == "" {
		audio_index = "0"
	}

	if ! strings.HasPrefix(audio_index, "m:language:") {
		if _, err := strconv.Atoi(audio_index); err != nil {
			log.Println("Bad audio index passed.")
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
	}

	seek := r.FormValue("ss")
	if seek == "" {
		seek = "0"
	}

	runcmd := []string{
		"-ss", seek,
		"-i", filename,
		"-loglevel", "error",
		"-map", "0:v:0",
		"-map", fmt.Sprintf("0:a:%s?", audio_index),
		"-crf", "35",
		"-c:a", "libopus",
		"-af", "aformat=channel_layouts='7.1|5.1|stereo'",
		"-c:v", "libvpx-vp9",
		"-deadline", "realtime",
		"-cpu-used", "8",
		"-row-mt", "1",
		"-f", "webm",
		"-filter:v", fmt.Sprintf("subtitles=filename='%s':force_style='Fontname=Roboto,PrimaryColour=&H00d5ff'", subname),
		"pipe:1",
	}

	w.Header().Set("Content-Type", "video/webm")
	w.Header().Set("Content-Disposition", "inline")
	w.Header()["Content-Length"] = nil // Stop go helpfully setting it to 0
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	log.Println(FfmpegBinary, runcmd)

	start := time.Now()
	cmd := exec.CommandContext(r.Context(), FfmpegBinary, runcmd...)
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Println(FfmpegBinary, runcmd)
		log.Println(err)
	}
	if time.Now().Sub(start).Minutes() >= 1 {
		log.Println("marking as watched")
		os.Chtimes(filename, time.Time{}, time.Unix(60, 0))
	}
}

func ManageConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		var known []TorrentConfig
		for _, t := range tclient.Torrents() {
			known = append(known, TorrentConfig{
				Magnet: t.Metainfo().Magnet(nil, nil).String(),
				Name:   t.Name(),
				Missing: t.BytesMissing(),
			})
		}
		enc := json.NewEncoder(w)
		enc.Encode(known)

	case http.MethodPost:
		r.ParseForm()
		log.Println(r.PostForm)

		for _, t := range tclient.Torrents() {
			l := t.Metainfo().Magnet(nil, nil).String()
			if !r.PostForm.Has(l) {
				log.Printf("Dropping %s\n", t.Name())
				t.Drop()
			}
		}

		for tc, _ := range r.PostForm {
			if tc == "new" {
				tc = r.PostForm.Get("new")
			}
			if tc == "" {
				continue
			}
			m, err := metainfo.ParseMagnetUri(tc)
			if err != nil {
				log.Println("Cant parse magnet link", err)
				continue
			}
			if _, ok := tclient.Torrent(m.InfoHash); ok {
				continue
			}
			t, err := tclient.AddMagnet(tc)
			if err != nil {
				log.Println("Cant add magnet link", err)
				continue
			}
			<-t.GotInfo()
			t.DownloadAll()
			log.Printf("Added %s\n", t.Name())
		}
		http.Redirect(w, r, "/", 303)
		go SaveMagnets()
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func LoadMagnets() {
	tlock.Lock()
	defer tlock.Unlock()
	f, err := os.Open(filepath.Join(DataDirectory, "magnets.json"))
	if err != nil {
		return
	}
	var magnets []string
	if err := json.NewDecoder(f).Decode(&magnets); err != nil {
		return
	}

	for _, magnet := range magnets {
		t, err := tclient.AddMagnet(magnet)
		if err != nil {
			log.Println("Cant add magnet link", err)
			continue
		}
		<-t.GotInfo()
		t.DownloadAll()
		log.Printf("Added %s\n", t.Name())
	}
}

func SaveMagnets() {
	tlock.Lock()
	defer tlock.Unlock()
	f, err := os.OpenFile(filepath.Join(DataDirectory, "magnets.json"),
		os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0700)
	if err != nil {
		return
	}
	var magnets []string

	for _, t := range tclient.Torrents() {
		magnets = append(magnets, t.Metainfo().Magnet(nil, nil).String())
	}

	if err := json.NewEncoder(f).Encode(&magnets); err != nil {
		return
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

	tconf := torrent.NewDefaultClientConfig()
	tconf.DataDir = DataDirectory
	tconf.DefaultStorage = storage.NewFile(DataDirectory)
	if c, err := torrent.NewClient(tconf); err == nil {
		tclient = c
	} else {
		log.Fatal(err)
	}
	defer tclient.Close()

	LoadMagnets()

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
	log.Println("Listening....")
	log.Fatal(http.ListenAndServe(*listen, logRequest(http.DefaultServeMux)))
}
