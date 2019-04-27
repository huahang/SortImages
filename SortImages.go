package main

import (
	"fmt"
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
				err = os.Rename(path, "./JPG/" + filename)
				err = checkError(err)
				if err != nil {
					return err
				}
				return nil
			}
			if rawExtensions[ext] {
				err = os.Rename(path, "./RAW/" + filename)
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
