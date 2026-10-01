package job

import "sync"

// ProgressIndefinite is the special percent value to indicate that the
// percent progress is not known.
const ProgressIndefinite float64 = -1

// Progress is used by JobExec to communicate updates to the job's progress to
// the JobManager.
//
// # A nil *Progress is usable and does nothing
//
// Every method here tolerates a nil receiver, so a JobExec can report progress
// unconditionally. That is what makes a job testable: `updater` is unexported
// and needs a *Manager and a *Job, so a test in any other package cannot build
// a real Progress at all -- and a job whose Execute dereferences it is a job
// whose tests have to run a whole job manager, or panic.
//
// The alternative is every job guarding every call with `if progress != nil`,
// which is a check that is never false in production and is therefore never
// true in tests either -- so the guard would be untested code in the only place
// it runs. A nil-safe method is the same amount of code, in one place, and the
// behaviour is exercised by every test of every job.
type Progress struct {
	defined      bool
	processed    int
	total        int
	percent      float64
	currentTasks []*task

	mutex   sync.Mutex
	updater *updater
}

type task struct {
	description string
}

func (p *Progress) updated() {
	if p == nil {
		return
	}
	var details []string
	for _, t := range p.currentTasks {
		details = append(details, t.description)
	}

	p.updater.updateProgress(p.percent, details)
}

// Indefinite sets the progress to an indefinite amount.
func (p *Progress) Indefinite() {
	if p == nil {
		return
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.defined = false
	p.total = 0
	p.calculatePercent()
}

// Definite notifies that the total is known.
func (p *Progress) Definite() {
	if p == nil {
		return
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.defined = true
	p.calculatePercent()
}

// SetTotal sets the total number of work units and sets definite to true.
// This is used to calculate the progress percentage.
func (p *Progress) SetTotal(total int) {
	if p == nil {
		return
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.total = total
	p.defined = true
	p.calculatePercent()
}

// AddTotal adds to the total number of work units. This is used to calculate the
// progress percentage.
func (p *Progress) AddTotal(total int) {
	if p == nil {
		return
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.total += total
	p.calculatePercent()
}

// SetProcessed sets the number of work units completed. This is used to
// calculate the progress percentage.
func (p *Progress) SetProcessed(processed int) {
	if p == nil {
		return
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.processed = processed
	p.calculatePercent()
}

func (p *Progress) calculatePercent() {
	switch {
	case !p.defined || p.total <= 0:
		p.percent = ProgressIndefinite
	case p.processed < 0:
		p.percent = 0
	default:
		p.percent = float64(p.processed) / float64(p.total)
		if p.percent > 1 {
			p.percent = 1
		}
	}

	p.updated()
}

// SetPercent sets the progress percent directly. This value will be
// overwritten if Indefinite, SetTotal, Increment or SetProcessed is called.
// Constrains the percent value between 0 and 1, inclusive.
func (p *Progress) SetPercent(percent float64) {
	if p == nil {
		return
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	if percent < 0 {
		percent = 0
	} else if percent > 1 {
		percent = 1
	}

	p.percent = percent
	p.updated()
}

// Increment increments the number of processed work units. This is used to calculate the percentage.
// If total is set already, then the number of processed work units will not exceed the total.
func (p *Progress) Increment() {
	if p == nil {
		return
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	if !p.defined || p.total <= 0 || p.processed < p.total {
		p.processed++
		p.calculatePercent()
	}
}

// AddProcessed increments the number of processed work units by the provided
// amount. This is used to calculate the percentage.
func (p *Progress) AddProcessed(v int) {
	if p == nil {
		return
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	newVal := v
	if p.defined && p.total > 0 && newVal > p.total {
		newVal = p.total
	}

	p.processed = newVal
	p.calculatePercent()
}

func (p *Progress) addTask(t *task) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.currentTasks = append([]*task{t}, p.currentTasks...)
	p.updated()
}

func (p *Progress) removeTask(t *task) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	for i, tt := range p.currentTasks {
		if tt == t {
			p.currentTasks = append(p.currentTasks[:i], p.currentTasks[i+1:]...)
			p.updated()
			return
		}
	}
}

// ExecuteTask executes a task as part of a job. The description is used to
// populate the Details slice in the parent Job.
func (p *Progress) ExecuteTask(description string, fn func()) {
	// The nil case still RUNS fn. Returning early here would report success
	// having done nothing, which is the worst possible failure for a job: the
	// user's library looks processed and no cluster was ever written. The
	// progress label is what is lost, not the work.
	if p == nil {
		fn()
		return
	}

	t := &task{
		description: description,
	}

	p.addTask(t)
	defer p.removeTask(t)
	fn()
}
