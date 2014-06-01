/*
  Copyright (c) 2012 José Carlos Nieto, http://xiam.menteslibres.org/

  Permission is hereby granted, free of charge, to any person obtaining
  a copy of this software and associated documentation files (the
  "Software"), to deal in the Software without restriction, including
  without limitation the rights to use, copy, modify, merge, publish,
  distribute, sublicense, and/or sell copies of the Software, and to
  permit persons to whom the Software is furnished to do so, subject to
  the following conditions:

  The above copyright notice and this permission notice shall be
  included in all copies or substantial portions of the Software.

  THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
  EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
  MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
  NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE
  LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION
  OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION
  WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
*/

package main

import (
	"bytes"
	"crypto"
	"errors"
	"flag"
	"fmt"
	"github.com/gosexy/checksum"
	"github.com/gosexy/exif"
	"github.com/gosexy/to"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"
)

const (
	Ps = string(os.PathSeparator)
	Fs = "-"
)

var pcount = 0

var ok chan int

var (
	ErrUnknownFile   = errors.New(`Could not identify file using EXIF data.`)
	ErrNotADirectory = errors.New(`%s: is not a directory.`)
)

var (
	reSpecialA        = regexp.MustCompile(`[áäâãà]`)
	reSpecialE        = regexp.MustCompile(`[éëêẽè]`)
	reSpecialI        = regexp.MustCompile(`[íïîĩì]`)
	reSpecialO        = regexp.MustCompile(`[óöôõò]`)
	reSpecialU        = regexp.MustCompile(`[úüûũù]`)
	reSpecialN        = regexp.MustCompile(`[ñ]`)
	reSpecialNotAlpha = regexp.MustCompile(`[^a-z0-9]`)
	reSpecialSpaces   = regexp.MustCompile(` +`)
	reDateTime        = regexp.MustCompile(`(\d{4}):(\d{2}):(\d{2}) (\d{2}):(\d{2}):(\d{2})`)
)

type Stats struct {
	copied  int
	moved   int
	skipped int
	deleted int
	unknown int
}

var stats Stats

var (
	flagFrom        = flag.String("from", "", "Media source directory.")
	flagDest        = flag.String("to", "", "Media destination directory.")
	flagMove        = flag.Bool("move", false, "Delete original file after copying? (move file).")
	flagDryRun      = flag.Bool("dry-run", false, "Prints what would be done without actually doing it?")
	flagMaxProcs    = flag.Int("max-procs", runtime.NumCPU(), "The maximum number of tasks running at the same time.")
	flagExifTool    = flag.Bool("exiftool", false, "Use exiftool instead of libexif (slower. requires exiftool http://owl.phy.queensu.ca/~phil/exiftool/).")
	flagTryExifTool = flag.Bool("try-exiftool", false, "Fallback to exiftool if libexif fails (requires exiftool http://owl.phy.queensu.ca/~phil/exiftool/).")
)

func getExifData(file string) (map[string]string, error) {
	var err error

	if *flagExifTool == false || *flagTryExifTool == true {

		ex := exif.New()

		err = ex.Open(file)
		if err == nil {
			return ex.Tags, nil
		}

	}

	if *flagExifTool == true || *flagTryExifTool == true {

		cmd := exec.Command("exiftool", file)

		var out bytes.Buffer
		cmd.Stdout = &out

		if err := cmd.Run(); err != nil {
			return nil, err
		}

		tags := make(map[string]string)

		data := strings.Trim(out.String(), " \r\n")
		lines := strings.Split(data, "\n")

		var k, v string
		for _, line := range lines {
			k = strings.TrimSpace(line[0:32])
			v = strings.TrimSpace(line[33:])
			tags[k] = v
		}

		return tags, nil
	}

	return nil, ErrUnknownFile
}

func verifyDirectory(dpath string) (err error) {
	var stat os.FileInfo
	if stat, err = os.Stat(dpath); err != nil {
		return err
	}
	if stat.IsDir() == false {
		return fmt.Errorf(ErrNotADirectory.Error(), dpath)
	}
	return nil
}

func copyFile(src string, dst string) (err error) {
	var input *os.File
	var output *os.File

	if input, err = os.Open(src); err != nil {
		return err
	}
	defer input.Close()

	if output, err = os.Create(dst); err != nil {
		return err
	}
	defer output.Close()

	_, err = io.Copy(output, input)

	return err
}

func fileMove(src string, dst string) error {
	var err error

	if err = os.Rename(src, dst); err != nil {

		if err = copyFile(src, dst); err != nil {
			return err
		}

		if err = os.Remove(src); err != nil {
			return err
		}

	}

	return nil
}

func textilize(input string) string {
	sc := unicode.SpecialCase{}

	output := strings.ToLowerSpecial(sc, input)

	output = reSpecialA.ReplaceAllLiteralString(output, "a")

	output = reSpecialE.ReplaceAllLiteralString(output, "e")

	output = reSpecialI.ReplaceAllLiteralString(output, "i")

	output = reSpecialO.ReplaceAllLiteralString(output, "o")

	output = reSpecialU.ReplaceAllLiteralString(output, "u")

	output = reSpecialN.ReplaceAllLiteralString(output, "n")

	output = reSpecialNotAlpha.ReplaceAllLiteralString(output, " ")

	output = reSpecialSpaces.ReplaceAllLiteralString(output, " ")

	output = strings.Replace(strings.TrimSpace(output), " ", "_", -1)

	return output
}

func normalize(chunks ...string) string {
	name := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk != "" {
			name = append(name, textilize(chunk))
		}
	}
	return strings.Join(name, Fs)
}

func pick(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func startImport(name string, dest string) {

	defer func() {
		ok <- 1
	}()

	tags, err := getExifData(name)

	if err == nil {

		hash := checksum.File(name, crypto.SHA1)
		rename := ""

		switch tags["File Type"] {

		case "MP3":

			rename = strings.Join(
				[]string{
					dest,
					normalize(pick(tags["Artist"], "Unknown Artist")),
					normalize(pick(tags["Album"], "Unknown Album")),
					fmt.Sprintf("%s%s", normalize(tags["Track"], fmt.Sprintf("%s-%s", pick(tags["Title"], "Unknown Title"), hash[0:4])), pick(strings.ToLower(path.Ext(name)), ".mp3")),
				},
				Ps,
			)

		default:
			var taken string

			dateTimeFields := []string{
				"Date and Time (Original)",
				"Date/Time Original",
				"Media Create Date",
				"Track Create Date",
				"Create Date",
			}

			for _, field := range dateTimeFields {
				if tags[field] != "" {
					taken = tags[field]
					break
				}
			}

			if taken == "" {
				stats.unknown++
				return
			}

			all := reDateTime.FindAllStringSubmatch(taken, -1)

			timeTaken := time.Date(
				int(to.Int64(all[0][1])),
				time.Month(int(to.Int64(all[0][2]))),
				int(to.Int64(all[0][3])),
				int(to.Int64(all[0][4])),
				int(to.Int64(all[0][5])),
				int(to.Int64(all[0][6])),
				0,
				time.UTC,
			)

			rename = strings.Join(
				[]string{
					dest,
					to.String(timeTaken.Year()),
					fmt.Sprintf("%02d-%s", timeTaken.Month(), timeTaken.Month()),
					fmt.Sprintf("%02d-%s", timeTaken.Day(), timeTaken.Weekday()),
					fmt.Sprintf("%02d%02d%02d-%s%s", timeTaken.Hour(), timeTaken.Minute(), timeTaken.Second(), strings.ToUpper(hash[0:4]), strings.ToLower(path.Ext(name))),
				},
				Ps,
			)
		}

		if rename != "" {

			_, err := os.Stat(rename)

			if err != nil {

				if *flagDryRun == false {
					err = os.MkdirAll(path.Dir(rename), os.ModeDir|0750)
					if err != nil {
						panic(err)
					}
				}
				err = nil
				if *flagMove == true {
					log.Printf("Moving file: %s -> %s\n", name, rename)
					if *flagDryRun == false {
						err = fileMove(name, rename)
						stats.moved++
					}
				} else {
					log.Printf("Copying file: %s -> %s\n", name, rename)
					if *flagDryRun == false {
						err = copyFile(name, rename)
						stats.copied++
					}
				}
				if err != nil {
					panic(err)
				}

			} else {
				rehash := checksum.File(rename, crypto.SHA1)

				if hash == rehash {
					log.Printf("Destination already exists: %s, removing original: %s (same file).\n", rename, name)
					os.Remove(name)
					stats.deleted++
				} else {
					log.Printf("Destination already exists: %s, skipping original: %s (files differ).\n", rename, name)
					stats.skipped++
				}
			}

		} else {
			stats.unknown++
		}

	} else {
		stats.unknown++
	}

}

func scandir(dirname string, dest string) (err error) {

	var stat os.FileInfo
	var dh *os.File
	var files []os.FileInfo

	if stat, err = os.Stat(dirname); err != nil {
		return err
	}

	if stat.IsDir() == false {
		return fmt.Errorf(ErrNotADirectory.Error(), dirname)
	}

	if dh, err = os.Open(dirname); err != nil {
		return err
	}

	defer dh.Close()

	if files, err = dh.Readdir(-1); err != nil {
		return err
	}

	for _, file := range files {

		name := dirname + Ps + file.Name()

		if file.IsDir() == true {

			scandir(name, dest)

		} else {
			if pcount >= *flagMaxProcs {
				// Waiting for one task to finish
				<-ok
				pcount--
			}
			go startImport(name, dest)
			// Task count
			pcount++
		}

	}

	return nil
}

func main() {

	flag.Parse()

	if *flagFrom == "" || *flagDest == "" {
		fmt.Printf("Photoy, by J. Carlos Nieto.\n\n")
		fmt.Printf("A command line tool for importing photos and media files.\n\n")
		fmt.Printf("Sample usage:\n\n\tphotopy -from /media/usb/DCIM -to ~/Photos -dry-run\n\n")
		flag.PrintDefaults()
		fmt.Println("")
	} else {
		var err error

		ok = make(chan int, *flagMaxProcs)

		err = verifyDirectory(*flagFrom)
		if err != nil {
			log.Println(err.Error())
			return
		}

		err = verifyDirectory(*flagDest)
		if err != nil {
			log.Println(err.Error())
			return
		}

		scandir(*flagFrom, *flagDest)

		// Waiting for all tasks to finish
		for i := 0; i < pcount; i++ {
			<-ok
		}

		fmt.Printf("Copied: %d, Moved: %d, Skipped: %d, Deleted: %d, Without EXIF data: %d\n", stats.copied, stats.moved, stats.skipped, stats.deleted, stats.unknown)
	}
}
