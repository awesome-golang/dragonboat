// Copyright 2017-2019 Lei Ni (nilei81@gmail.com)
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/*
Package tests contains various helper functions and modules used in tests.

This package is internally used by Dragonboat, applications are not expected to
import this package.
*/
package tests

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/lni/dragonboat/internal/tests/kvpb"
	"github.com/lni/dragonboat/internal/utils/random"
	"github.com/lni/dragonboat/statemachine"
)

// random delays
func generateRandomDelay() {
	v := rand.Uint64()
	if v%10000 == 0 {
		time.Sleep(300 * time.Millisecond)
	} else if v%1000 == 0 {
		time.Sleep(100 * time.Millisecond)
	} else if v%100 == 0 {
		time.Sleep(10 * time.Millisecond)
	} else if v%20 == 0 {
		time.Sleep(2 * time.Millisecond)
	}
}

func getLargeRandomDelay() uint64 {
	// in IO error injection test, we don't want such delays
	ioei := os.Getenv("IOEI")
	if len(ioei) > 0 {
		return 0
	}
	v := rand.Uint64() % 100
	if v == 0 {
		return 30 * 1000
	}
	if v < 10 {
		return 1 * 1000
	}
	if v < 30 {
		return 500
	}
	if v < 50 {
		return 100
	}
	return 50
}

// KVTest is a in memory key-value store struct used for testing purposes.
// Note that both key/value are suppose to be valid utf-8 strings.
type KVTest struct {
	ClusterID        uint64            `json:"-"`
	NodeID           uint64            `json:"-"`
	KVStore          map[string]string `json:"KVStore"`
	Count            uint64            `json:"Count"`
	Junk             []byte            `json:"Junk"`
	closed           bool
	aborted          bool `json:"-"`
	externalFileTest bool
	pbkvPool         *sync.Pool
}

// NewKVTest creates and return a new KVTest object.
func NewKVTest(clusterID uint64, nodeID uint64) statemachine.IStateMachine {
	fmt.Println("kvtest with stoppable snapshot created")
	s := &KVTest{
		KVStore:   make(map[string]string),
		ClusterID: clusterID,
		NodeID:    nodeID,
		Junk:      make([]byte, 3*1024),
	}
	v := os.Getenv("EXTERNALFILETEST")
	s.externalFileTest = len(v) > 0
	fmt.Printf("junk data inserted, external file test %t\n", s.externalFileTest)
	// write some junk data consistent across the cluster
	for i := 0; i < len(s.Junk); i++ {
		s.Junk[i] = 2
	}
	s.pbkvPool = &sync.Pool{
		New: func() interface{} {
			return &kvpb.PBKV{}
		},
	}

	return s
}

// Lookup performances local looks up for the sepcified data.
func (s *KVTest) Lookup(key []byte) []byte {
	if s.closed {
		panic("lookup called after Close()")
	}

	if s.aborted {
		panic("Lookup() called after abort set to true")
	}
	v, ok := s.KVStore[string(key)]
	generateRandomDelay()
	if ok {
		return []byte(v)
	}

	return []byte("")
}

// Update updates the object using the specified committed raft entry.
func (s *KVTest) Update(data []byte) uint64 {
	s.Count++
	if s.aborted {
		panic("update() called after abort set to true")
	}
	if s.closed {
		panic("update called after Close()")
	}
	generateRandomDelay()
	dataKv := s.pbkvPool.Get().(*kvpb.PBKV)
	err := proto.Unmarshal(data, dataKv)
	if err != nil {
		panic(err)
	}
	s.updateStore(dataKv.GetKey(), dataKv.GetVal())
	s.pbkvPool.Put(dataKv)

	return uint64(len(data))
}

func (s *KVTest) saveExternalFile(fileCollection statemachine.ISnapshotFileCollection) {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	rn := random.LockGuardedRand.Uint64()
	fn := fmt.Sprintf("external-%d-%d-%d-%d.data",
		s.ClusterID, s.NodeID, s.Count, rn)
	fp := filepath.Join(dir, fn)
	f, err := os.Create(fp)
	if err != nil {
		panic(err)
	}
	content := fmt.Sprintf("external-test-data-%d", s.Count)
	_, err = f.Write([]byte(content))
	if err != nil {
		panic(err)
	}
	if err = f.Close(); err != nil {
		panic(err)
	}
	fmt.Printf("adding an external file, path %s", fp)
	fileCollection.AddFile(1, fp, []byte(content))
}

func checkExternalFile(files []statemachine.SnapshotFile, clusterID uint64) {
	if len(files) != 1 {
		panic("snapshot external file missing")
	}
	fr := files[0]
	if fr.FileID != 1 {
		panic("FileID value not expected")
	}
	wcontent := string(fr.Metadata)
	content, err := ioutil.ReadFile(fr.Filepath)
	if err != nil {
		panic(err)
	}
	if string(content) != wcontent {
		panic(fmt.Sprintf("unexpected external file content got %s, want %s, fp %s",
			string(content), wcontent, fr.Filepath))
	}
	log.Printf("external file check done")
}

// SaveSnapshot saves the current object state into a snapshot using the
// specified io.Writer object.
func (s *KVTest) SaveSnapshot(w io.Writer,
	fileCollection statemachine.ISnapshotFileCollection,
	done <-chan struct{}) (uint64, error) {
	if s.closed {
		panic("save snapshot called after Close()")
	}
	if s.externalFileTest {
		s.saveExternalFile(fileCollection)
	}

	delay := getLargeRandomDelay()
	fmt.Printf("random delay %d ms\n", delay)
	for delay > 0 {
		delay -= 10
		time.Sleep(10 * time.Millisecond)
		select {
		case <-done:
			return 0, statemachine.ErrSnapshotStopped
		default:
		}
	}
	data, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	n, err := w.Write(data)
	if err != nil {
		return 0, err
	}
	if n != len(data) {
		panic("didn't write the whole data buf")
	}

	return uint64(len(data)), nil
}

// RecoverFromSnapshot recovers the state using the provided snapshot.
func (s *KVTest) RecoverFromSnapshot(r io.Reader,
	files []statemachine.SnapshotFile,
	done <-chan struct{}) error {
	if s.closed {
		panic("recover from snapshot called after Close()")
	}

	if s.externalFileTest {
		checkExternalFile(files, s.ClusterID)
	}

	delay := getLargeRandomDelay()
	fmt.Printf("random delay %d ms\n", delay)
	for delay > 0 {
		delay -= 10
		time.Sleep(10 * time.Millisecond)
		select {
		case <-done:
			s.aborted = true
			return statemachine.ErrSnapshotStopped
		default:
		}
	}

	var store KVTest
	data, err := ioutil.ReadAll(r)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &store); err != nil {
		return err
	}
	if store.aborted {
		panic("snapshot image contains aborted==true")
	}
	s.KVStore = store.KVStore
	s.Count = store.Count
	s.Junk = store.Junk
	return nil
}

// Close closes the IStateMachine instance
func (s *KVTest) Close() {
	s.closed = true
	log.Printf("%d:%dKVStore has been closed", s.ClusterID, s.NodeID)
}

// GetHash returns a uint64 representing the current object state.
func (s *KVTest) GetHash() uint64 {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}

	hash := md5.New()
	if _, err = hash.Write(data); err != nil {
		panic(err)
	}
	md5sum := hash.Sum(nil)
	return binary.LittleEndian.Uint64(md5sum[:8])
}

func (s *KVTest) updateStore(key string, value string) {
	s.KVStore[key] = value
}
