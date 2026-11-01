package backupjob

import (
	"context"
	"errors"
	"io"
)

var errFTPUnavailable = errors.New("backupjob: FTP target not available in this build")

type ftpStub struct{}

// NewFTP returns the FTP target.
func NewFTP(FTPSettings) Target { return ftpStub{} }

func (ftpStub) Put(context.Context, string, io.Reader, int64) error { return errFTPUnavailable }
func (ftpStub) Size(context.Context, string) (int64, error)         { return 0, errFTPUnavailable }
func (ftpStub) List(context.Context) ([]string, error)              { return nil, errFTPUnavailable }
func (ftpStub) Delete(context.Context, string) error                { return errFTPUnavailable }
func (ftpStub) Test(context.Context) error                          { return errFTPUnavailable }
