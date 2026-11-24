package api

// FolderQuery asks an engine for the folders in Path on its machine. Path
// is absolute, or ~ for home and ~/x under it; "" is home.
type FolderQuery struct {
	Path string `json:"path,omitempty"`
}

// FolderList is the folders in one folder on an engine's machine.
type FolderList struct {
	// Path is the folder listed, absolute and clean.
	Path string `json:"path"`
	// Home is the engine user's home folder.
	Home string `json:"home"`
	// Parent is the folder Path is in, "" at the root.
	Parent  string   `json:"parent,omitempty"`
	Folders []Folder `json:"folders"`
	// Truncated says Path holds more folders than Folders lists.
	Truncated bool `json:"truncated,omitempty"`
}

// Folder is one folder in a FolderList.
type Folder struct {
	Name string `json:"name"`
	// Repo says the folder holds a .git.
	Repo bool `json:"repo,omitempty"`
}

// RepoPage is one page of the GitHub repositories the engine machine's
// gh is signed in to reach, most recently pushed first. The last page
// has Last set, and Truncated when more than the listing's cap exist.
type RepoPage struct {
	Repos     []Repo `json:"repos"`
	Last      bool   `json:"last,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Repo is one repository in a RepoPage.
type Repo struct {
	Name        string `json:"name"` // owner/name
	Description string `json:"description,omitempty"`
	Private     bool   `json:"private,omitempty"`
	Fork        bool   `json:"fork,omitempty"`
}

// CloneRequest asks an engine to clone Source, a URL or owner/name, into
// Dest on its machine: a folder, absolute or under ~, that isn't there yet
// or is empty.
type CloneRequest struct {
	Source string `json:"source"`
	Dest   string `json:"dest"`
}

// Cloned is where a clone went, as an absolute path.
type Cloned struct {
	Path string `json:"path"`
}
