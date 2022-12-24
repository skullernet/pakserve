# pakutil

Simple utility for listing content of PAK files, extracting and creating PAK
files, and converting PAK files into ZIP (.pkz) archives without extraction.

## Parameters

* `-l <pak>` List pak contents.
* `-c <pak> <dir>` Create pak from dir.
* `-x <pak> <dir>` Extract pak into dir.
* `-z <pak> <pkz>` Convert pak to pkz.
* `-u <pkz> <pak>` Convert pkz to pak.

## Notes

* When creating and extracting .pak files all file names are converted to lower
  case.
* Creation and extracting of .pkz is not supported. Use specialized ZIP archive
  tools for that.
