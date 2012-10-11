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
	"crypto"
	"flag"
	"fmt"
	"github.com/gosexy/checksum"
	"github.com/gosexy/exif"
	"github.com/gosexy/to"
	"io"
	"log"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
)

const PS = string(os.PathSeparator)

var dateTimeFields = []string{
	"Date and Time (Original)",
}

var flagFrom = flag.String("from", "", "Photos source directory.")
var flagDest = flag.String("to", "", "Photos destination directory.")
var flagMove = flag.Bool("move", false, "Delete original file after copying.")
var flagDryRun = flag.Bool("dry-run", false, "Just prints what would be done without actually doing it.")

func verifyDirectory(name string) error {
	stat, err := os.Stat(name)
	if err != nil {
		return err
	}
	if stat.IsDir() == false {
		return fmt.Errorf("%s: is not a directory.", *flagFrom)
	}
	return nil
}

func Copy(src string, dst string) error {
	var err error

	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer output.Close()

	_, err = io.Copy(output, input)

	return err
}

func Move(src string, dst string) error {
	var err error

	err = os.Rename(src, dst)

	if err != nil {

		err = Copy(src, dst)

		if err != nil {
			return err
		}

		return os.Remove(src)
	}

	return nil
}

func Import(name string, dest string) {

	ex := exif.New()

	re, _ := regexp.Compile(`(\d{4}):(\d{2}):(\d{2}) (\d{2}):(\d{2}):(\d{2})`)

	err := ex.Open(name)

	if err == nil {

		var taken string

		for _, field := range dateTimeFields {
			if ex.Tags[field] != "" {
				taken = ex.Tags[field]
				break
			}
		}

		if taken != "" {

			all := re.FindAllStringSubmatch(taken, -1)

			timeTaken := time.Date(
				to.Int(all[0][1]),
				time.Month(to.Int(all[0][2])),
				to.Int(all[0][3]),
				to.Int(all[0][4]),
				to.Int(all[0][5]),
				to.Int(all[0][6]),
				0,
				time.UTC,
			)

			hash := checksum.File(name, crypto.SHA1)

			rename := strings.Join(
				[]string{
					dest,
					to.String(timeTaken.Year()),
					to.String(timeTaken.Month()),
					fmt.Sprintf("%02d-%s", timeTaken.Day(), timeTaken.Weekday()),
					fmt.Sprintf("%02d%02d%02d-%s", timeTaken.Hour(), timeTaken.Minute(), timeTaken.Second(), strings.ToUpper(hash[0:4])),
				},
				PS,
			)

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
						err = Move(name, rename)
					}
				} else {
					log.Printf("Copying file: %s -> %s\n", name, rename)
					if *flagDryRun == false {
						err = Copy(name, rename)
					}
				}
				if err != nil {
					panic(err)
				}
			} else {
				log.Printf("Skipping file: %s\n", rename)
			}

		}

	}

}

func Scandir(dirname string, dest string) error {

	var err error

	stat, err := os.Stat(dirname)

	if err != nil {
		return err
	}

	if stat.IsDir() == false {
		return fmt.Errorf("Not a directory.")
	}

	dh, err := os.Open(dirname)

	if err != nil {
		return err
	}

	defer dh.Close()

	files, err := dh.Readdir(-1)

	if err != nil {
		return err
	}

	for _, file := range files {

		name := dirname + PS + file.Name()

		if file.IsDir() == true {
			Scandir(name, dest)
		} else {
			Import(name, dest)
		}

	}

	return nil
}

func main() {

	flag.Parse()

	if *flagFrom == "" || *flagDest == "" {
		fmt.Printf("Sample usage: phosphor -from /run/media/me/usb/DCIM -to ~/Photos -dry-run\n")
		flag.PrintDefaults()
	} else {
		var err error

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

		Scandir(*flagFrom, *flagDest)
	}
}
