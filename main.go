package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	_ "embed"
)

//go:embed index.html
var indexpage []byte

var extensions = map[string]bool{
	".webm": true,
	".mkv":  true,
	".flv":  true,
	".vob":  true,
	".ogv":  true,
	".ogg":  true,
	".drc":  true,
	".gifv": true,
	".mng":  true,
	".avi":  true,
	".mts":  true,
	".m2ts": true,
	".ts":   true,
	".mov":  true,
	".qt":   true,
	".wmv":  true,
	".yuv":  true,
	".rm":   true,
	".rmvb": true,
	".viv":  true,
	".asf":  true,
	".amv":  true,
	".mp4":  true,
	".m4p":  true,
	".m4v":  true,
	".mpg":  true,
	".mp2":  true,
	".mpeg": true,
	".mpe":  true,
	".mpv":  true,
	".m2v":  true,
	".svi":  true,
	".3gp":  true,
	".3g2":  true,
	".mxf":  true,
	".roq":  true,
	".nsv":  true,
	".f4v":  true,
	".f4p":  true,
	".f4a":  true,
	".f4b":  true,
}

var DataDirectory string
var FfmpegBinary string

func index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "text/html")
	w.Write(indexpage)
}

func list(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "text/plain")
	err := filepath.Walk(DataDirectory,
		func(fn string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			ex := path.Ext(fn)
			if _, ok := extensions[strings.ToLower(ex)]; ok {
				fmt.Fprintln(w, fn)
			}
			return nil
		})
	if err != nil {
		log.Println(err)
	}
}

func encodeVideo(w http.ResponseWriter, r *http.Request) {
	filename := r.FormValue("fn")
	if filename == "" {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	if _, err := filepath.Rel(DataDirectory, filename); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Has local SRT file?
	subname := filename
	if ex := path.Ext(filename); ex != "" {
		srtp := fmt.Sprintf("%s.srt", strings.TrimSuffix(filename, path.Ext(filename)))
		if fi, err := os.Stat(srtp); err == nil && !fi.IsDir() {
			subname = srtp
		}
	}

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
	}

	if !r.Form.Has("nosub") {
		vf := fmt.Sprintf("subtitles=filename='%s'", subname)
		if r.Form.Has("si") {
			vf = fmt.Sprintf("%s:si=%s", vf, r.FormValue("si"))
		}
		runcmd = append(runcmd, vf)
	}

	// Final output arg
	runcmd = append(runcmd, "pipe:1")

	w.Header().Set("Content-Type", "video/webm;codecs=\"vp8,vorbis\"")
	w.Header().Set("Content-Disposition", "inline")
	w.Header()["Content-Length"] = nil // Stop go helpfully setting it to 0
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

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

	http.HandleFunc("/", index)
	http.HandleFunc("/list", list)
	http.HandleFunc("/video", encodeVideo)

	log.Fatal(http.ListenAndServe(*listen, logRequest(http.DefaultServeMux)))
}
