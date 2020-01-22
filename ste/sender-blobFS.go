// Copyright © Microsoft <wastore@microsoft.com>
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package ste

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/Azure/azure-pipeline-go/pipeline"

	"github.com/Azure/azure-storage-azcopy/azbfs"
	"github.com/Azure/azure-storage-azcopy/common"
)

type blobFSSenderBase struct {
	jptm                IJobPartTransferMgr
	fileOrDirURL        URLHolder
	chunkSize           uint32
	numChunks           uint32
	pipeline            pipeline.Pipeline
	pacer               pacer
	creationTimeHeaders *azbfs.BlobFSHTTPHeaders
	flushThreshold      int64
}

func newBlobFSSenderBase(jptm IJobPartTransferMgr, destination string, p pipeline.Pipeline, pacer pacer, sip ISourceInfoProvider) (*blobFSSenderBase, error) {

	info := jptm.Info()

	// compute chunk size and number of chunks
	chunkSize := info.BlockSize
	numChunks := getNumChunks(info.SourceSize, chunkSize)

	// make sure URL is parsable
	destURL, err := url.Parse(destination)
	if err != nil {
		return nil, err
	}

	props, err := sip.Properties()
	if err != nil {
		return nil, err
	}
	headers := props.SrcHTTPHeaders.ToBlobFSHTTPHeaders()

	var h URLHolder
	if info.IsFolderPropertiesTransfer() {
		h = azbfs.NewDirectoryURL(*destURL, p)
	} else {
		h = azbfs.NewFileURL(*destURL, p)
	}
	return &blobFSSenderBase{
		jptm:                jptm,
		fileOrDirURL:        h,
		chunkSize:           chunkSize,
		numChunks:           numChunks,
		pipeline:            p,
		pacer:               pacer,
		creationTimeHeaders: &headers,
	}, nil
}

func (u *blobFSSenderBase) fileURL() azbfs.FileURL {
	return u.fileOrDirURL.(azbfs.FileURL)
}

func (u *blobFSSenderBase) dirURL() azbfs.DirectoryURL {
	return u.fileOrDirURL.(azbfs.DirectoryURL)
}

func (u *blobFSSenderBase) SendableEntityType() common.EntityType {
	if _, ok := u.fileOrDirURL.(azbfs.DirectoryURL); ok {
		panic("not supported yet")
		return common.EEntityType.Folder()
	} else {
		return common.EEntityType.File()
	}
}

func (u *blobFSSenderBase) ChunkSize() uint32 {
	return u.chunkSize
}

func (u *blobFSSenderBase) NumChunks() uint32 {
	return u.numChunks
}

func (u *blobFSSenderBase) RemoteFileExists() (bool, error) {
	return remoteObjectExists(u.fileURL().GetProperties(u.jptm.Context()))
}

func (u *blobFSSenderBase) Prologue(state common.PrologueState) (destinationModified bool) {

	u.flushThreshold = int64(u.chunkSize) * int64(ADLSFlushThreshold)

	// Create file with the source size
	destinationModified = true
	_, err := u.fileURL().Create(u.jptm.Context(), *u.creationTimeHeaders) // note that "create" actually calls "create path"
	if err != nil {
		u.jptm.FailActiveUpload("Creating file", err)
		return
	}
	return
}

func (u *blobFSSenderBase) Cleanup() {
	jptm := u.jptm

	// Cleanup if status is now failed
	if jptm.IsDeadInflight() {
		// transfer was either failed or cancelled
		// the file created in share needs to be deleted, since it's
		// contents will be at an unknown stage of partial completeness
		deletionContext, cancelFn := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancelFn()
		_, err := u.fileURL().Delete(deletionContext)
		if err != nil {
			jptm.Log(pipeline.LogError, fmt.Sprintf("error deleting the (incomplete) file %s. Failed with error %s", u.fileURL().String(), err.Error()))
		}
	}
}

func (u *blobFSSenderBase) GetDestinationLength() (int64, error) {
	prop, err := u.fileURL().GetProperties(u.jptm.Context())

	if err != nil {
		return -1, err
	}

	return prop.ContentLength(), nil
}
