package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"gitlab.alpinelinux.org/alpine/go/repository"
)

var (
	remoteURL  string
	mountPoint string
)

type RemoteFile struct {
	fs.Inode
	remoteURL string
	content   []byte
	once      sync.Once
	size      uint64
}

func (f *RemoteFile) fetchContent() {
	resp, err := http.Get(f.remoteURL)
	if err != nil {
		log.Printf("Failed to fetch content: %v", err)
		return
	}
	defer resp.Body.Close()

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Failed to read content: %v", err)
		return
	}
	f.content = content
}

func (f *RemoteFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0644
	out.Size = f.size
	return 0
}

func (f *RemoteFile) Read(ctx context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	f.once.Do(f.fetchContent)

	end := int(off) + len(dest)
	if end > len(f.content) {
		end = len(f.content)
	}

	return fuse.ReadResultData(f.content[off:end]), 0
}

func (f *RemoteFile) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	return nil, fuse.FOPEN_KEEP_CACHE, fs.OK
}

type Root struct {
	fs.Inode
}

func (r *Root) OnAdd(ctx context.Context) {
	url := path.Join(remoteURL, "APKINDEX.tar.gz")
	resp, err := http.Get("https://" + url)
	if err != nil {
		fmt.Println(err)
	}
	defer resp.Body.Close()

	index := getIndex(resp.Body)
	apkIndex, err := repository.ParsePackageIndex(index)
	if err != nil {
		fmt.Println(err)
	}
	for i, p := range apkIndex {
		fileName := p.Name + "-" + p.Version + ".apk"
		remoteUrl := "https://" + remoteURL + "/" + fileName
		r.AddChild(
			fileName,
			r.NewPersistentInode(ctx,
				&RemoteFile{remoteURL: remoteUrl, size: uint64(p.Size)},
				fs.StableAttr{
					Ino:  uint64(i + 1),
					Mode: 0644,
				}), false)
	}
}

func getIndex(r io.Reader) io.Reader {
	unzipped, err := gzip.NewReader(r)
	if err != nil {
		fmt.Println(err)
	}

	tr := tar.NewReader(unzipped)
	for {
		th, err := tr.Next()
		if err == io.EOF {
			break
		}
		if th.Name == "APKINDEX" {
			return tr
		}
	}
	return nil
}

func main() {
	flag.StringVar(&mountPoint, "mount-point", "/apkindex", "path to mount point")
	flag.StringVar(&remoteURL, "repo-url", "packages.wolfi.dev/os/aarch64", "URL to repositorys")

	flag.Parse()

	root := &Root{}
	opts := &fs.Options{}
	opts.Debug = false
	server, err := fs.Mount(mountPoint, root, opts)
	if err != nil {
		fmt.Printf("Mount fail: %v\n", err)
		os.Exit(1)
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for range c {
			server.Unmount()
		}
	}()

	server.Wait()

}
