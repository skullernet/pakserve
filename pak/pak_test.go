package pak

import (
	"bytes"
	"io/ioutil"
	"testing"
)

func TestReadWrite(t *testing.T) {
	w, err := OpenWriter("test.pak")
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}

	names := []string{"foo", "bar", "123qwe"}
	contents := []string{"test", "junkjunkjunk", ""}

	for i, v := range names {
		err = w.Create(v)
		if err != nil {
			t.Fatalf("create file: %v", err)
		}
		_, err := w.Write([]byte(contents[i]))
		if err != nil {
			t.Fatalf("write file: %v", err)
		}
	}
	err = w.Close()
	if err != nil {
		t.Fatalf("close writer: %v", err)
	}

	r, err := OpenReader("test.pak")
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}

	for i, v := range names {
		if r.File[i].Name != v {
			t.Fatalf("unexpected file name: %s", r.File[i].Name)
		}
		b, err := ioutil.ReadAll(r.File[i].Open())
		if err != nil {
			t.Fatalf("read file: %v", err)
		}
		if bytes.Compare(b, []byte(contents[i])) != 0 {
			t.Fatalf("unexpected data")
		}
	}
	err = r.Close()
	if err != nil {
		t.Fatalf("close reader: %v", err)
	}
}
