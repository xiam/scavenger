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
	"os"
	"log"
	"flag"
	"fmt"
	"github.com/gosexy/canvas"
	"github.com/gosexy/checksum"
	"path"
	"crypto"
	"strings"
	"io"
)

const PS = string(os.PathSeparator)

var flagFrom = flag.String("from", "", "Photos source directory.")
var flagDest = flag.String("to", "", "Photos destination directory.")

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

func Move(src string, dst string) error {
	var err error

	err = os.Rename(src, dst)

	if err != nil {
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

		if err != nil {
			return err
		}

		return os.Remove(src)
	}

	return nil
}

func Import(name string, dest string) {

	img := canvas.New()

	if img.Open(name) == true {

		defer img.Destroy()

		meta := img.Metadata()

		if meta["exif:DateTimeOriginal"] != "" {

			datetime := strings.Split(meta["exif:DateTimeOriginal"], " ")

			date := strings.Split(datetime[0], ":")
			time := strings.Split(datetime[1], ":")

			hash := checksum.File(name, crypto.SHA1)

			rename := dest + PS + strings.Join(date, PS) + PS + strings.Join(time, "") + strings.ToUpper(hash[0:5]) + strings.ToLower(path.Ext(name))

			_, err := os.Stat(rename)

			if err != nil {
				os.MkdirAll(path.Dir(rename), os.ModeDir | 0750)
				err = Move(name, rename)
				log.Printf("Moving file: %s -> %s\n", name, rename)
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
		fmt.Printf("Sample usage: phosphor -from /run/media/me/usb -to ~/Photos\n")
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
