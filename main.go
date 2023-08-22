package main

import (
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
	"strings"

	"golang.org/x/net/webdav"

	_ "embed"
)

//go:embed index.html
var indexpage []byte

//go:embed blank.ass
var blanksubs []byte

var DataDirectory string
var FfmpegBinary string
var davHandler *webdav.Handler

func index(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		w.Header().Set("content-type", "text/html")
		w.Write(indexpage)
		return
	}
	if r.Method == http.MethodGet {
		ex := path.Ext(r.URL.Path)
		if strings.HasPrefix(mime.TypeByExtension(ex), "video/") {
			EncodeVideo(filepath.Join(DataDirectory, r.URL.Path), w, r)
			return
		}
	}

	davHandler.ServeHTTP(w, r)
}

func ExtractSubsOrBlank(filename, sub_index string, destination *os.File) {
	subcmd := exec.CommandContext(context.Background(),
		FfmpegBinary, "-y",
		"-i", filename,
		"-loglevel", "error",
		"-map", fmt.Sprintf("0:s:%s", sub_index),
		"-f", "ass",
		destination.Name())
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
		"-b:v", "1M",
		"-crf", "10",
		"-c:a", "libvorbis",
		"-c:v", "libvpx",
		"-deadline", "realtime",
		"-cpu-used", "8",
		"-row-mt", "1",
		"-f", "webm",
		"-filter:v", fmt.Sprintf("subtitles=filename='%s'", subname),
		"pipe:1",
	}

	w.Header().Set("Content-Type", "video/webm;codecs=\"vp8,vorbis\"")
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

func TestSubtitleStream(filename string, stream int) bool {
	// ffmpeg -i video -c copy -map 0:s:0 -frames:s 1 -f null - -v 0 -hide_banner
	runcmd := []string{
		"-i", filename,
		"-loglevel", "panic",
		"-c", "copy",
		"-map", "0:s:0",
		"-frames:s", "1",
		"-f", "null",
		"-",
	}
	cmd := exec.CommandContext(context.Background(), FfmpegBinary, runcmd...)
	return cmd.Run() == nil
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
