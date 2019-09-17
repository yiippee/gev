// +build linux

package poller

import (
	"log"

	"github.com/Allenxuxu/toolkit/sync/atomic"
	"golang.org/x/sys/unix"
)

const readEvent = unix.EPOLLIN | unix.EPOLLPRI
const writeEvent = unix.EPOLLOUT

type Poller struct {
	fd       int
	eventFd  int
	running  atomic.Bool
	waitDone chan struct{}
}

func Create() (*Poller, error) {
	fd, err := unix.EpollCreate1(0)
	if err != nil {
		return nil, err
	}

	r0, _, errno := unix.Syscall(unix.SYS_EVENTFD2, 0, 0, 0)
	if errno != 0 {
		return nil, errno
	}
	eventFd := int(r0)

	err = unix.EpollCtl(fd, unix.EPOLL_CTL_ADD, eventFd, &unix.EpollEvent{
		Events: unix.EPOLLIN,
		Fd:     int32(eventFd),
	})
	if err != nil {
		_ = unix.Close(fd)
		_ = unix.Close(eventFd)
		return nil, err
	}

	return &Poller{
		fd:       fd,
		eventFd:  eventFd,
		waitDone: make(chan struct{}),
	}, nil
}

var wakeBytes = []byte{1, 0, 0, 0, 0, 0, 0, 0}

func (ep *Poller) Wake() error {
	_, err := unix.Write(ep.eventFd, wakeBytes)
	return err
}

var buf = make([]byte, 8)

func (ep *Poller) wakeHandlerRead() {
	n, err := unix.Read(ep.eventFd, buf)
	if err != nil || n != 8 {
		log.Println("wakeHandlerRead", err, n)
	}
}

func (ep *Poller) Close() (err error) {
	if !ep.running.Get() {
		return ErrClosed
	}

	ep.running.Set(false)
	if err = ep.Wake(); err != nil {
		return
	}

	<-ep.waitDone
	_ = unix.Close(ep.fd)
	_ = unix.Close(ep.eventFd)
	return
}

func (ep *Poller) add(fd int, events uint32) error {
	return unix.EpollCtl(ep.fd, unix.EPOLL_CTL_ADD, fd, &unix.EpollEvent{
		Events: events,
		Fd:     int32(fd),
	})
}

func (ep *Poller) AddRead(fd int) error {
	return ep.add(fd, readEvent)
}

func (ep *Poller) AddWrite(fd int) error {
	return ep.add(fd, writeEvent)
}

func (ep *Poller) Del(fd int) error {
	return unix.EpollCtl(ep.fd, unix.EPOLL_CTL_DEL, fd, nil)
}

func (ep *Poller) mod(fd int, events uint32) error {
	return unix.EpollCtl(ep.fd, unix.EPOLL_CTL_MOD, fd, &unix.EpollEvent{
		Events: events,
		Fd:     int32(fd),
	})
}

func (ep *Poller) EnableReadWrite(fd int) error {
	return ep.mod(fd, readEvent|writeEvent)
}

func (ep *Poller) EnableWrite(fd int) error {
	return ep.mod(fd, writeEvent)
}

func (ep *Poller) EnableRead(fd int) error {
	return ep.mod(fd, readEvent)
}

func (ep *Poller) Poll(handler func(fd int, event Event)) {
	defer func() {
		close(ep.waitDone)
	}()

	events := make([]unix.EpollEvent, waitEventsBegin)
	var wake bool
	ep.running.Set(true)
	for {
		n, err := unix.EpollWait(ep.fd, events, -1)

		if err != nil && err != unix.EINTR {
			panic("EpollWait: " + err.Error())
		}

		for i := 0; i < n; i++ {
			fd := int(events[i].Fd)
			if fd != ep.eventFd {
				var rEvents Event
				if ((events[i].Events & unix.POLLHUP) != 0) && ((events[i].Events & unix.POLLIN) == 0) {
					rEvents |= EventErr
				}
				if (events[i].Events&unix.EPOLLERR != 0) || (events[i].Events&unix.EPOLLOUT != 0) {
					rEvents |= EventWrite
				}
				if events[i].Events&(unix.EPOLLIN|unix.EPOLLPRI|unix.EPOLLRDHUP) != 0 {
					rEvents |= EventRead
				}

				handler(fd, rEvents)
			} else {
				ep.wakeHandlerRead()

				if !ep.running.Get() {
					goto quit
				}
				wake = true
			}
		}

		if wake {
			handler(-1, 0)
			wake = false
		}

		if n == len(events) {
			events = make([]unix.EpollEvent, n*2)
		}
	}
quit:
}
