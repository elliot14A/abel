package workflow

type File struct {
	Path     string
	Name     string
	Env      map[string]string
	Defaults Defaults
	Jobs     map[string]Job
	JobIDs   []string
}

func (f File) Job(id string) (Job, bool) {
	j, ok := f.Jobs[id]
	return j, ok
}

type Defaults struct {
	Shell            string
	WorkingDirectory string
}

type Job struct {
	ID          string
	Name        string
	RunsOn      []string
	Container   Container
	Env         map[string]string
	Defaults    Defaults
	Steps       []Step
	Needs       []string
	If          string
	HasStrategy bool
	Line        int
}

type Container struct {
	Image   string
	Env     map[string]string
	Options string
}

type Step struct {
	Name             string
	Uses             string
	Run              string
	Shell            string
	WorkingDirectory string
	If               string
	Env              map[string]string
	Line             int
}

func (s Step) IsRun() bool { return s.Run != "" }

func (s Step) Label() string {
	switch {
	case s.Name != "":
		return s.Name
	case s.Uses != "":
		return s.Uses
	default:
		return firstLine(s.Run, 60)
	}
}

func firstLine(s string, maxLen int) string {
	for i, r := range s {
		if r == '\n' {
			s = s[:i]
			break
		}
	}
	if len(s) > maxLen {
		return s[:maxLen-1] + "…"
	}
	return s
}
