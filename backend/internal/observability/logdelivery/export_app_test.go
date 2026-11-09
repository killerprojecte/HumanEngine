package logdelivery

import (
	"io"
	"os"
	"sync"
)

// HeldFileOwnerForAppTest is compiled only into the package's test variant.
// The external native integration test imports that variant plus app. No
// production registry API or runtime injection capability is introduced.
type HeldFileOwnerForAppTest struct {
	Registry                   *Registry
	WriteEntered, CloseEntered <-chan struct{}
	mu                         sync.Mutex
	files                      []*os.File
	channels                   []*Channel
}

func NewHeldFileOwnerForAppTest(stdout io.Writer, writeGate, closeGate <-chan struct{}) *HeldFileOwnerForAppTest {
	writeEntered, closeEntered := make(chan struct{}), make(chan struct{})
	probe := &HeldFileOwnerForAppTest{WriteEntered: writeEntered, CloseEntered: closeEntered}
	var writeOnce, closeOnce sync.Once
	r := NewRegistry(stdout)
	probe.Registry = r
	r.factory = func(options channelOptions, exit func()) (*Channel, error) {
		var output sink = newStdoutSink(stdout)
		if options.Directory != "" {
			output = &fileSink{directory: options.Directory, rotateBytes: options.RotateBytes, writeFile: func(file *os.File, data []byte) (int, error) {
				writeOnce.Do(func() {
					probe.mu.Lock()
					probe.files = append(probe.files, file)
					probe.mu.Unlock()
					close(writeEntered)
				})
				<-writeGate
				return writeFileDescriptor(file, data)
			}, closeFile: func(file *os.File) error {
				closeOnce.Do(func() { close(closeEntered) })
				<-closeGate
				return file.Close()
			}}
		}
		channel := startChannel(options, output, nil, exit)
		probe.mu.Lock()
		probe.channels = append(probe.channels, channel)
		probe.mu.Unlock()
		return channel, nil
	}
	return probe
}
func (p *HeldFileOwnerForAppTest) DescriptorStatErrors() []error {
	p.mu.Lock()
	files := append([]*os.File(nil), p.files...)
	p.mu.Unlock()
	result := make([]error, len(files))
	for i, file := range files {
		_, result[i] = file.Stat()
	}
	return result
}
func (p *HeldFileOwnerForAppTest) OwnerDone() []<-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]<-chan struct{}, len(p.channels))
	for i, c := range p.channels {
		result[i] = c.Done()
	}
	return result
}
