package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func checkError(err error) error {
	if err != nil {
		fmt.Printf("[Error] Hit an error! " + err.Error() + "\n")
	}
	return err
}

func CopyFile(dstName, srcName string) (err error) {
	src, err := os.Open(srcName)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(dstName, os.O_WRONLY | os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, src)
	return err
}

func main() {
	args := os.Args
	if len(args) != 2 {
		fmt.Printf("Usage: SortImages [path to scan]\n")
		return
	}
	err := os.MkdirAll("./JPG", 0755)
	_ = checkError(err)
	err = os.MkdirAll("./RAW", 0755)
	_ = checkError(err)
	var jpegExtensions = make(map[string]bool)
	jpegExtensions[".jpg"] = true
	jpegExtensions[".jpeg"] = true
	var rawExtensions = make(map[string]bool)
	rawExtensions[".raf"] = true
	rawExtensions[".dng"] = true
	rawExtensions[".orf"] = true
	rawExtensions[".arw"] = true
	_ = filepath.Walk(args[1], func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return checkError(err)
		}
		if info.Mode().IsRegular() {
			ext := strings.ToLower(filepath.Ext(path))
			filename := filepath.Base(path)
			if jpegExtensions[ext] {
				err = CopyFile("./JPG/" + filename, path)
				err = checkError(err)
				if err != nil {
					return err
				}
				return nil
			}
			if rawExtensions[ext] {
				err = CopyFile("./RAW/" + filename, path)
				err = checkError(err)
				if err != nil {
					return err
				}
				return nil
			}
		}
		return nil
	})

}
