package rocks

import (
	"fmt"
	"github.com/chrislusf/glog"
	"github.com/chrislusf/gorocksdb"
	"io/ioutil"
	"os"
)

func (d *Rocks) addSst(name string, next func() (bool, []byte, []byte)) error {

	return d.AddSstByWriter(name, func(w *gorocksdb.SSTFileWriter) (int64, error) {
		var counter int64
		var hasNext bool
		var key, value []byte
		for {
			hasNext, key, value = next()
			if !hasNext {
				break
			}
			if err := w.Add(key, value); err != nil {
				return counter, fmt.Errorf("write sst file: %v", err)
			}
			counter++
		}
		return counter, nil
	})

}

// AddSstByWriter add SST by ingesting behind
func (d *Rocks) AddSstByWriter(name string, writerFunc func(*gorocksdb.SSTFileWriter) (int64, error)) error {
	envOpts := gorocksdb.NewDefaultEnvOptions()
	defer envOpts.Destroy()
	opts := gorocksdb.NewDefaultOptions()
	defer opts.Destroy()
	w := gorocksdb.NewSSTFileWriter(envOpts, opts)
	defer w.Destroy()

	filePath, err := ioutil.TempFile("", "sst-file-")
	if err != nil {
		return fmt.Errorf("get temp file: %v", err)
	}
	defer func() {
		if err != nil {
			os.Remove(filePath.Name())
		}
	}()

	err = w.Open(filePath.Name())
	if err != nil {
		return fmt.Errorf("open temp file: %v", err)
	}

	var counter int64
	counter, err = writerFunc(w)
	if err != nil {
		return fmt.Errorf("write: %v", err)
	}
	if counter == 0 {
		return nil
	}
	glog.V(1).Infof("%s: added %d entries", name, counter)

	err = w.Finish()
	if err != nil {
		return fmt.Errorf("finish sst file: %v", err)
	}

	ingestOpts := gorocksdb.NewDefaultIngestExternalFileOptions()
	defer ingestOpts.Destroy()
	ingestOpts.SetMoveFiles(true)
	ingestOpts.SetIngestionBehind(true)
	ingestOpts.SetAllowGlobalSeqNo(true)
	err = d.db.IngestExternalFile([]string{filePath.Name()}, ingestOpts)
	if err != nil {
		return fmt.Errorf("%s: db %s ingest sst file %s: %v", name, d.path, filePath.Name(), err)
	}
	glog.V(1).Infof("%s: db %s ingested %s", name, d.path, filePath.Name())

	return nil
}
