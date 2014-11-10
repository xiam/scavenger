// Copyright (c) 2012-2014 José Carlos Nieto, https://menteslibres.net/xiam
//
// Permission is hereby granted, free of charge, to any person obtaining
// a copy of this software and associated documentation files (the
// "Software"), to deal in the Software without restriction, including
// without limitation the rights to use, copy, modify, merge, publish,
// distribute, sublicense, and/or sell copies of the Software, and to
// permit persons to whom the Software is furnished to do so, subject to
// the following conditions:
//
// The above copyright notice and this permission notice shall be
// included in all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
// EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
// MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
// NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE
// LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION
// OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION
// WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

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
	pathSeparator = string(os.PathSeparator)
	fileSeparator = "-"
)

var pcount = 0

var ok chan int

var (
	ErrUnknownFile       = errors.New(`Could not identify file using EXIF data.`)
	ErrNotADirectory     = errors.New(`%s: is not a directory.`)
	ErrMissingCreateTime = errors.New(`Missing create time.`)
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

// Attempts to retrieve EXIF data from a file.
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

// Returns nil is the path is a directory.
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

// Copies a file into a new name.
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

// Changes the name of the file.
func moveFile(src string, dst string) error {
	var err error

	// Attempt to rename.
	if err = os.Rename(src, dst); err != nil {

		// If the file could not be renamed copy and remove it.
		if err = copyFile(src, dst); err != nil {
			return err
		}

		if err = os.Remove(src); err != nil {
			return err
		}

	}

	return nil
}

// Returns a normalized version of a string.
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

	output = strings.Replace(strings.TrimSpace(output), " ", fileSeparator, -1)

	return output
}

// Returns a normalized version of the input slice of strings.
func normalize(chunks ...string) string {
	name := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk != "" {
			name = append(name, textilize(chunk))
		}
	}
	return strings.Join(name, fileSeparator)
}

// Returns the first non-empty value.
func pick(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func getExifCreateDate(tags map[string]string) (time.Time, error) {
	var taken string
	var ok bool

	// Looking for the first tag that sounds like a date.
	dateTimeFields := []string{
		"Date and Time (Original)",
		"Date/Time Original",
		"Create Date",
	}

	for _, field := range dateTimeFields {
		if taken, ok = tags[field]; ok {
			break
		}
	}

	if taken == "" {
		return time.Time{}, ErrMissingCreateTime
	}

	all := reDateTime.FindAllStringSubmatch(taken, -1)

	t := time.Date(
		int(to.Int64(all[0][1])),
		time.Month(int(to.Int64(all[0][2]))),
		int(to.Int64(all[0][3])),
		int(to.Int64(all[0][4])),
		int(to.Int64(all[0][5])),
		int(to.Int64(all[0][6])),
		0,
		time.Local,
	)

	return t, nil
}

// File processor.
func processFile(name string, dest string) {

	var tags map[string]string
	var err error

	defer func() {
		ok <- 1
	}()

	if tags, err = getExifData(name); err != nil {
		stats.unknown++
		return
	}

	hash := checksum.File(name, crypto.SHA1)
	rename := ""

	// What kind of file is this?

	if _, ok := tags["Track"]; ok {
		// Is it a music file?
		rename = strings.Join(
			[]string{
				dest,
				normalize(pick(tags["Artist"], "Unknown Artist")),
				normalize(pick(tags["Album"], "Unknown Album")),
				fmt.Sprintf("%s%s", normalize(tags["Track"], fmt.Sprintf("%s-%s", pick(tags["Title"], "Unknown Title"), hash[0:8])), strings.ToLower(path.Ext(name))),
			},
			pathSeparator,
		)
		goto OK
	}

	if _, ok := tags["Camera Model Name"]; ok {
		// Is it a digital photo file?
		var timeTaken time.Time

		if timeTaken, err = getExifCreateDate(tags); err != nil {
			stats.unknown++
			return
		}

		rename = strings.Join(
			[]string{
				dest,
				strings.ToUpper(normalize(tags["Camera Model Name"])),
				strings.ToUpper(normalize(tags["File Type"])),
				to.String(timeTaken.Year()),
				fmt.Sprintf("%02d-%s", timeTaken.Month(), timeTaken.Month()),
				fmt.Sprintf("%02d-%s", timeTaken.Day(), timeTaken.Weekday()),
				fmt.Sprintf("%02d%02d%02d-%s%s", timeTaken.Hour(), timeTaken.Minute(), timeTaken.Second(), strings.ToUpper(hash[0:8]), strings.ToLower(path.Ext(name))),
			},
			pathSeparator,
		)
		goto OK
	}

	if _, ok := tags["Vendor ID"]; ok {
		// Is a special file.
		var timeTaken time.Time

		if timeTaken, err = getExifCreateDate(tags); err != nil {
			stats.unknown++
			return
		}

		rename = strings.Join(
			[]string{
				dest,
				strings.ToUpper(normalize(tags["Vendor ID"])),
				strings.ToUpper(normalize(tags["File Type"])),
				to.String(timeTaken.Year()),
				fmt.Sprintf("%02d-%s", timeTaken.Month(), timeTaken.Month()),
				fmt.Sprintf("%02d-%s", timeTaken.Day(), timeTaken.Weekday()),
				fmt.Sprintf("%02d%02d%02d-%s%s", timeTaken.Hour(), timeTaken.Minute(), timeTaken.Second(), strings.ToUpper(hash[0:8]), strings.ToLower(path.Ext(name))),
			},
			pathSeparator,
		)
		goto OK
	}

	// Unknown file.

	/*
		if _, ok := tags["File Type"]; ok {
			// Is a regular file.
			rename = strings.Join(
				[]string{
					dest,
					strings.ToUpper(normalize(tags["File Type"])),
					fmt.Sprintf("%s-%s", strings.ToUpper(hash[0:8]), path.Base(name)),
				},
				pathSeparator,
			)
			goto OK
		}
	*/

	/*
		rename = strings.Join(
			[]string{
				dest,
				strings.ToUpper(path.Ext(name)),
				fmt.Sprintf("%s%s", strings.ToUpper(hash[0:8]), strings.ToLower(path.Ext(name))),
			},
			pathSeparator,
		)
	*/

OK:

	if rename == "" {
		stats.unknown++
		return
	}

	_, err = os.Stat(rename)

	if err == nil {
		// A file with the same destination name already exists.

		rehash := checksum.File(rename, crypto.SHA1)

		if hash == rehash {
			// Destination file is the same.
			log.Printf("Destination already exists: %s, removing original: %s (same file).\n", rename, name)
			// Remove original.
			if *flagDryRun == false {
				os.Remove(name)
				stats.deleted++
			}
		} else {
			// Destination file is different from source, don't know what to do,
			// better skip it.
			log.Printf("Destination already exists: %s, skipping original: %s (files differ).\n", rename, name)
			stats.skipped++
		}

	} else {
		// Preparing to create the new file.

		if *flagDryRun == false {
			if err = os.MkdirAll(path.Dir(rename), os.ModeDir|0750); err != nil {
				panic(err)
			}
		}

		if *flagMove == true {
			// User wants to remove the original file.
			log.Printf("Moving file: %s -> %s\n", name, rename)
			if *flagDryRun == false {
				if err = moveFile(name, rename); err != nil {
					panic(err)
				}
				stats.moved++
			}
		} else {
			// User wants to create a copy of the original file.
			log.Printf("Copying file: %s -> %s\n", name, rename)
			if *flagDryRun == false {
				if err = copyFile(name, rename); err != nil {
					panic(err)
				}
				stats.copied++
			}
		}

	}

}

func processDirectory(sourcedir string, destdir string) (err error) {

	var stat os.FileInfo
	var files []os.FileInfo

	// Verifying source directory.
	if stat, err = os.Stat(sourcedir); err != nil {
		return err
	}

	if stat.IsDir() == false {
		return fmt.Errorf(ErrNotADirectory.Error(), sourcedir)
	}

	// Verifying destination directory.
	var dir *os.File
	if dir, err = os.Open(sourcedir); err != nil {
		return err
	}

	defer dir.Close()

	// Listing files in source directory.
	if files, err = dir.Readdir(-1); err != nil {
		return err
	}

	// File in directory.
	for _, file := range files {

		filepath := sourcedir + pathSeparator + file.Name()

		if file.IsDir() == true {

			// Recursive import.
			processDirectory(filepath, destdir)

		} else {

			if pcount >= *flagMaxProcs {
				// Waiting for one task to finish
				<-ok
				pcount--
			}

			// Processing file.
			go processFile(filepath, destdir)

			// Task count
			pcount++

		}

	}

	return nil
}

func main() {

	flag.Parse()

	if *flagFrom == "" || *flagDest == "" {

		// Not all requisites were met.

		fmt.Printf("Photopy, by J. Carlos Nieto.\n\n")
		fmt.Printf("A command line tool for importing photos and media files into a sane file layout.\n\n")
		fmt.Printf("Sample usage:\n\n\tphotopy -from /Volumes/external -to ~/Photos -dry-run\n\n")

		flag.PrintDefaults()

		fmt.Printf("\n")

	} else {
		var err error

		ok = make(chan int, *flagMaxProcs)

		// Verifying source directory.
		if err = verifyDirectory(*flagFrom); err != nil {
			log.Fatalf(err.Error())
			return
		}

		// Verifying destination directory.
		if err = verifyDirectory(*flagDest); err != nil {
			log.Fatalf(err.Error())
			return
		}

		// Scanning
		processDirectory(*flagFrom, *flagDest)

		// Waiting for all tasks to finish
		for i := 0; i < pcount; i++ {
			<-ok
		}

		// Execution summary.
		log.Printf("Copied: %d, Moved: %d, Skipped: %d, Deleted: %d, Without EXIF data: %d\n", stats.copied, stats.moved, stats.skipped, stats.deleted, stats.unknown)
	}
}
