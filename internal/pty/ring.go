package pty

// ringBuf is a fixed-capacity circular byte buffer that tracks cumulative byte
// offsets across wraps so reattaching or paused subscribers can resume cleanly.
type ringBuf struct {
	buf  []byte
	cap  int
	head int
	size int
	base uint64
}

func newRing(capacity int) *ringBuf {
	if capacity <= 0 {
		capacity = DefaultReplayBytes
	}
	return &ringBuf{
		buf: make([]byte, capacity),
		cap: capacity,
	}
}

func (r *ringBuf) end() uint64 {
	return r.base + uint64(r.size)
}

func (r *ringBuf) write(p []byte) uint64 {
	if len(p) == 0 {
		return r.end()
	}
	if len(p) >= r.cap {
		tail := p[len(p)-r.cap:]
		copy(r.buf, tail)
		r.base += uint64(r.size + (len(p) - r.cap))
		r.head = 0
		r.size = r.cap
		return r.end()
	}
	if overflow := r.size + len(p) - r.cap; overflow > 0 {
		r.head = (r.head + overflow) % r.cap
		r.size -= overflow
		r.base += uint64(overflow)
	}
	tail := (r.head + r.size) % r.cap
	n1 := min(len(p), r.cap-tail)
	copy(r.buf[tail:], p[:n1])
	if n1 < len(p) {
		copy(r.buf[0:], p[n1:])
	}
	r.size += len(p)
	return r.end()
}

func (r *ringBuf) readFrom(after uint64) (out []byte, end uint64, truncated bool) {
	end = r.end()
	if after < r.base {
		after = r.base
		truncated = true
	}
	if after >= end {
		return nil, end, truncated
	}
	avail := int(end - after)
	out = make([]byte, avail)
	startOff := int(after - r.base)
	pos := (r.head + startOff) % r.cap
	n1 := min(avail, r.cap-pos)
	copy(out, r.buf[pos:pos+n1])
	if n1 < avail {
		copy(out[n1:], r.buf[:avail-n1])
	}
	return out, end, truncated
}
