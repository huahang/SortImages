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

func copyFile(dstName, srcName string) (err error) {
	src, err := os.Open(srcName)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(dstName, os.O_WRONLY|os.O_CREATE, 0644)
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
	err = os.MkdirAll("./MP4", 0755)
	_ = checkError(err)
	err = os.MkdirAll("./HEIC", 0755)
	_ = checkError(err)
	err = os.MkdirAll("./Unknown", 0755)
	_ = checkError(err)
	var jpegExtensions = make(map[string]bool)
	jpegExtensions[".jpg"] = true
	jpegExtensions[".jpeg"] = true
	var rawExtensions = make(map[string]bool)
	rawExtensions[".raf"] = true
	rawExtensions[".dng"] = true
	rawExtensions[".orf"] = true
	rawExtensions[".arw"] = true
	var mp4Extensions = make(map[string]bool)
	mp4Extensions[".mp4"] = true
	var heicExtensions = make(map[string]bool)
	heicExtensions[".heic"] = true
	heicExtensions[".heif"] = true
	_ = filepath.Walk(args[1], func(path string, info os.FileInfo, err error) error {
		if os.IsPermission(err) {
			fmt.Printf("[Warning] No permission: " + path + "\n")
			return nil
		}
		if err != nil {
			return checkError(err)
		}
		if info.Mode().IsRegular() {
			ext := strings.ToLower(filepath.Ext(path))
			filename := filepath.Base(path)
			if jpegExtensions[ext] {
				err = copyFile("./JPG/"+filename, path)
				err = checkError(err)
				if err != nil {
					return err
				}
				return nil
			} else if rawExtensions[ext] {
				err = copyFile("./RAW/"+filename, path)
				err = checkError(err)
				if err != nil {
					return err
				}
				return nil
			} else if mp4Extensions[ext] {
				err = copyFile("./MP4/"+filename, path)
				err = checkError(err)
				if err != nil {
					return err
				}
				return nil
			} else if heicExtensions[ext] {
				err = copyFile("./HEIC/"+filename, path)
				err = checkError(err)
				if err != nil {
					return err
				}
				return nil
			} else {
				err = copyFile("./Unknown/"+filename, path)
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
