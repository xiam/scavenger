# Scanvenger

Scanvenger is a command line tool that imports media files into a nice directory
layout.

```
brew install libexif exiftool
```

Example:

```
scanvenger -from /media/UsbStick -to ~/Photos -max-procs 3 -dry-run -try-exiftool
```

The directory layout is guessed from the EXIF creation date. EXIF metadata is
provided by `libexif` or `exiftool`.

This is an example of how the final directory layout would look like:

```
cd ~/Photos
find .
.
./2011
./2011/03-March
./2011/03-March/06-Sunday
./2011/03-March/06-Sunday/092033-ABC2.jpg
./2011/03-March/06-Sunday/092229-DC31.jpg
./2011/03-March/13-Sunday
./2011/03-March/13-Sunday/021937-C2EB.jpg
./2011/03-March/13-Sunday/040807-95A3.jpg
./2011/03-March/13-Sunday/040823-9CD8.jpg
./2011/10-October/25-Tuesday
./2011/10-October/25-Tuesday/214139-C762.jpg
./2011/10-October/25-Tuesday/214342-3619.jpg
...
```

## Install scanvenger

```
go get github.com/xiam/scanvenger
scanvenger -help
```

## Getting exiftool

If you want `scanvenger` to be able to read metadata for almost any kind of file
(not just photos) you'll need `exiftool`. Download and install it from
http://owl.phy.queensu.ca/~phil/exiftool/

> Copyright (c) 2012 José Carlos Nieto, http://xiam.menteslibres.org/
>
> Permission is hereby granted, free of charge, to any person obtaining
> a copy of this software and associated documentation files (the
> "Software"), to deal in the Software without restriction, including
> without limitation the rights to use, copy, modify, merge, publish,
> distribute, sublicense, and/or sell copies of the Software, and to
> permit persons to whom the Software is furnished to do so, subject to
> the following conditions:
>
> The above copyright notice and this permission notice shall be
> included in all copies or substantial portions of the Software.
>
> THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
> EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
> MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
> NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE
> LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION
> OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION
> WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
