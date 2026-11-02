package protocol

// QueueAssembler joins the pages of a reserved-song list.
//
// The device sends a list longer than ten entries as several queue frames, ten
// entries each, with start and end giving each page's place in the whole list:
// a list of eleven arrives as 0–10 then 10–11. This applies both to replies to
// Queryorder and to the lists it pushes unprompted.
//
// The zero value is ready to use. It is not safe for concurrent use.
type QueueAssembler struct {
	pending *Queue
}

// Add takes one queue frame and returns the whole list once its last page has
// arrived, or nil while pages are still missing.
//
// A page that does not continue the list being assembled — a different total,
// or a start that does not follow on from the previous page — is dropped along
// with the partial list, and assembly restarts at the next first page.
func (a *QueueAssembler) Add(q *Queue) *Queue {
	if q.Start == 0 {
		a.pending = &Queue{
			Seq:     q.Seq,
			Total:   q.Total,
			Entries: append([]QueueEntry(nil), q.Entries...),
		}
	} else if p := a.pending; p != nil && p.Total == q.Total && len(p.Entries) == q.Start {
		p.Seq = q.Seq
		p.Entries = append(p.Entries, q.Entries...)
	} else {
		a.pending = nil
		return nil
	}

	p := a.pending
	p.End = len(p.Entries)
	// An empty page past the first cannot advance the list; give up rather than
	// wait for it forever.
	if p.End >= p.Total || (q.Start > 0 && len(q.Entries) == 0) {
		a.pending = nil
		return p
	}
	return nil
}
