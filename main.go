package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	ffmpeg "github.com/u2takey/ffmpeg-go"

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
	".gif":  true,
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

	kw := ffmpeg.KwArgs{
		// "filter:v": "scale=720",
		"loglevel": "error",
		"c:a":      "libvorbis",
		"c:v":      "libvpx-vp9",
		"deadline": "realtime",
		"cpu-used": "8",
		"row-mt":   "1",
		"format":   "webm",
	}

	if !r.Form.Has("nosub") {
		kw["vf"] = fmt.Sprintf("subtitles=filename='%s'", subname)
		if r.Form.Has("si") {
			kw["vf"] = fmt.Sprintf("%s:si=%s", kw["vf"], r.FormValue("si"))
		}
	}

	w.Header().Set("Content-Type", "video/webm;codecs=\"vp9,vorbis\"")
	w.Header().Set("Content-Disposition", "inline")
	w.Header()["Content-Length"] = nil // Stop go helpfully setting it to 0
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	errb := &bytes.Buffer{}

	_ = ffmpeg.Input(filename).Output("pipe:1", kw).WithOutput(w, errb).Run()

	if errb.Len() > 0 && !bytes.Contains(errb.Bytes(), []byte("Broken pipe")) {
	// if errb.Len() > 0 {
		log.Println(errb.String())
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
	flag.Parse()

	http.HandleFunc("/", index)
	http.HandleFunc("/list", list)
	http.HandleFunc("/video", encodeVideo)

	log.Fatal(http.ListenAndServe(":8080", logRequest(http.DefaultServeMux)))

}
