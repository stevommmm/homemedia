package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"path/filepath"

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

func index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "text/html")
	w.Write(indexpage)
}

func list(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "text/plain")
	err := filepath.Walk(".",
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

	kw := ffmpeg.KwArgs{
		"c:a":      "libvorbis",
		"c:v":      "libtheora",
		"qscale:a": "3",
		"qscale:v": "3",
		"format":   "ogv",
	}

	if !r.Form.Has("nosub") {
		kw["vf"] = fmt.Sprintf("subtitles=%s", filename)
		if r.Form.Has("si") {
			kw["vf"] = fmt.Sprint("%s,si=%s", kw["vm"], r.FormValue("si"))
		}
	}

	_ = &bytes.Buffer{}
	w.Header().Set("content-type", "video/ogg")
	err := ffmpeg.Input(filename).
		Output("pipe:1", kw).
		WithOutput(w, os.Stdout).Run()
	log.Println(err)
	log.Println("ffmpeg process1 done")
	// buf.WriteTo(w)
}

func main() {
	http.HandleFunc("/", index)
	http.HandleFunc("/list", list)
	http.HandleFunc("/video", encodeVideo)

	log.Fatal(http.ListenAndServe(":8080", nil))

}
