package layout

import (
	"image"
	"image/draw"
	"sync"

	"github.com/faiface/gui"
)

// Mux can be used to multiplex an Env, let's call it a root Env. Mux implements a way to
// create multiple virtual Envs that all interact with the root Env. They receive the same
// events apart from gui.Resize, and their draw functions get redirected to the root Env.
//
// All gui.Resize events are instead modified according to the underlying Layout.
// The master Env gets the original gui.Resize events.
type Mux struct {
	mu         sync.Mutex
	lastResize gui.Event
	eventsIns  []chan<- gui.Event
	draw       chan<- func(draw.Image) image.Rectangle

	evIn   chan<- gui.Event
	layout Layout
}

// NewMux should only be used internally by Layouts.
// It has mostly the same behaviour as gui.Mux, except for its use of and underlying Layout
// for modifying the gui.Resize events. to the childs.
func NewMux(env gui.Env, l Layout) (mux *Mux, master gui.Env) {
	drawChan := make(chan func(draw.Image) image.Rectangle)
	mux = &Mux{
		layout: l,
		draw:   drawChan,
	}
	master, masterIn := mux.makeEnv(true)
	events := make(chan gui.Event, 0)
	mux.evIn = events
	go func() {
		for d := range drawChan {
			env.Draw() <- d
		}
		close(env.Draw())
	}()

	go func() {
		for e := range env.Events() {
			events <- e
		}
	}()

	go func() {
		for e := range events {
			// master gets a copy of all events to the Mux
			masterIn <- e
			mux.mu.Lock()
			if resize, ok := e.(gui.Resize); ok {
				mux.lastResize = resize

				rect := resize.Rectangle

				// Redraw self
				mux.draw <- func(drw draw.Image) image.Rectangle {
					mux.layout.Redraw(drw, rect)
					return rect
				}

				// Send appropriate resize Events to childs
				lay := mux.layout.Lay(rect)
				for i, eventsIn := range mux.eventsIns {
					resize.Rectangle = lay[i]
					eventsIn <- resize
				}

			} else {
				for _, eventsIn := range mux.eventsIns {
					eventsIn <- e
				}
			}
			mux.mu.Unlock()
		}
		mux.mu.Lock()
		for _, eventsIn := range mux.eventsIns {
			close(eventsIn)
		}
		mux.mu.Unlock()
	}()
	return
}

func (mux *Mux) MakeEnv() gui.Env {
	env, _ := mux.makeEnv(false)
	return env
}

type muxEnv struct {
	events <-chan gui.Event
	draw   chan<- func(draw.Image) image.Rectangle
}

func (m *muxEnv) Events() <-chan gui.Event                      { return m.events }
func (m *muxEnv) Draw() chan<- func(draw.Image) image.Rectangle { return m.draw }

// We do not store master env
func (mux *Mux) makeEnv(master bool) (env gui.Env, eventsIn chan<- gui.Event) {
	eventsOut, eventsIn := gui.MakeEventsChan()
	drawChan := make(chan func(draw.Image) image.Rectangle)
	env = &muxEnv{eventsOut, drawChan}

	if !master {
		mux.mu.Lock()
		mux.eventsIns = append(mux.eventsIns, eventsIn)
		// make sure to always send a resize event to a new Env if we got the size already
		// that means it missed the resize event by the root Env
		if mux.lastResize != nil {
			eventsIn <- mux.lastResize
		}
		mux.mu.Unlock()
	}

	go func() {
		func() {
			// When the master Env gets its Draw() channel closed, it closes all the Events()
			// channels of all the children Envs, and it also closes the internal draw channel
			// of the Mux. Otherwise, closing the Draw() channel of the master Env wouldn't
			// close the Env the Mux is muxing. However, some child Envs of the Mux may still
			// send some drawing commmands before they realize that their Events() channel got
			// closed.
			//
			// That is perfectly fine if their drawing commands simply get ignored. This down here
			// is a little hacky, but (I hope) perfectly fine solution to the problem.
			//
			// When the internal draw channel of the Mux gets closed, the line marked with ! will
			// cause panic. We recover this panic, then we receive, but ignore all furhter draw
			// commands, correctly draining the Env until it closes itself.
			defer func() {
				if recover() != nil {
					for range drawChan {
					}
				}
			}()
			for d := range drawChan {
				mux.draw <- d // !
			}
		}()
		if master {
			mux.mu.Lock()
			for _, eventsIn := range mux.eventsIns {
				close(eventsIn)
			}
			mux.eventsIns = nil
			close(mux.draw)
			mux.mu.Unlock()
		} else {
			mux.mu.Lock()
			i := -1
			for i = range mux.eventsIns {
				if mux.eventsIns[i] == eventsIn {
					break
				}
			}
			if i != -1 {
				mux.eventsIns = append(mux.eventsIns[:i], mux.eventsIns[i+1:]...)
			}
			mux.mu.Unlock()
			mux.evIn <- mux.lastResize
		}
	}()

	return env, eventsIn
}