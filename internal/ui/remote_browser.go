package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gu1llaum-3/sshm/internal/transfer"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// BrowserMode defines whether we're selecting files or directories
type BrowserMode int

const (
	BrowseFiles BrowserMode = iota
	BrowseDirectories
)

// searchDebounceTime is how long to wait after typing before searching
const searchDebounceTime = 400 * time.Millisecond

// remoteBrowserModel is the TUI file browser for remote files
type remoteBrowserModel struct {
	host        string
	configFile  string
	currentDir  string
	files       []transfer.RemoteFile // All files from directory
	visibleFiles []transfer.RemoteFile // Filtered files (respects showHidden)
	cursor      int
	selected    string
	err         string
	loading     bool
	mode        BrowserMode
	styles      Styles
	width       int
	height      int
	session     *transfer.SFTPSession
	searchMode  bool
	searchQuery string
	searchFiles []transfer.RemoteFile // Search results
	hasLocate   bool                  // Whether locate is available on remote
	showHidden  bool                  // Whether to show dotfiles

	// Debounce state
	pendingSearch   string // Query waiting to be searched
	searchTriggered bool   // Whether a search has been triggered for current query
}

// remoteBrowserResultMsg is sent when browsing is complete
type remoteBrowserResultMsg struct {
	path     string
	selected bool
	err      error
}

// remoteBrowserLoadedMsg is sent when directory listing completes
type remoteBrowserLoadedMsg struct {
	files []transfer.RemoteFile
	dir   string
	err   error
}

// remoteBrowserSearchMsg is sent when search completes
type remoteBrowserSearchMsg struct {
	files []transfer.RemoteFile
	query string
	err   error
}

// searchDebounceMsg is sent after debounce delay to trigger actual search
type searchDebounceMsg struct {
	query string
}

// NewRemoteBrowser creates a new remote file browser
func NewRemoteBrowser(host, startPath, configFile string, mode BrowserMode, styles Styles, width, height int) *remoteBrowserModel {
	if startPath == "" {
		startPath = "~"
	}

	return &remoteBrowserModel{
		host:       host,
		configFile: configFile,
		currentDir: startPath,
		mode:       mode,
		styles:     styles,
		width:      width,
		height:     height,
		loading:    true,
		cursor:     0,
	}
}

func (m *remoteBrowserModel) Init() tea.Cmd {
	return m.loadDirectory(m.currentDir)
}

func (m *remoteBrowserModel) loadDirectory(path string) tea.Cmd {
	return func() tea.Msg {
		// Create SFTP session if needed
		if m.session == nil {
			session, err := transfer.NewSFTPSession(m.host, m.configFile)
			if err != nil {
				return remoteBrowserLoadedMsg{err: err}
			}
			m.session = session
		}

		// Expand ~ on first load
		if path == "~" {
			home, err := m.session.GetHomeDirectory()
			if err == nil {
				path = home
			}
		}

		files, err := m.session.ListDirectory(path)
		if err != nil {
			return remoteBrowserLoadedMsg{err: err}
		}

		return remoteBrowserLoadedMsg{files: files, dir: path}
	}
}

func (m *remoteBrowserModel) runSearch() tea.Cmd {
	query := m.searchQuery
	return func() tea.Msg {
		if m.session == nil {
			return remoteBrowserSearchMsg{err: fmt.Errorf("no session"), query: query}
		}

		files, err := m.session.QuickSearch(query, m.currentDir, 30)
		if err != nil {
			return remoteBrowserSearchMsg{err: err, query: query}
		}

		return remoteBrowserSearchMsg{files: files, query: query}
	}
}

func (m *remoteBrowserModel) scheduleSearch(query string) tea.Cmd {
	return tea.Tick(searchDebounceTime, func(t time.Time) tea.Msg {
		return searchDebounceMsg{query: query}
	})
}

// filterFiles updates visibleFiles based on showHidden setting
func (m *remoteBrowserModel) filterFiles() {
	if m.showHidden {
		m.visibleFiles = m.files
		return
	}

	m.visibleFiles = nil
	for _, f := range m.files {
		// Always show ".." for navigation
		if f.Name == ".." || !strings.HasPrefix(f.Name, ".") {
			m.visibleFiles = append(m.visibleFiles, f)
		}
	}
}

func (m *remoteBrowserModel) Update(msg tea.Msg) (*remoteBrowserModel, tea.Cmd) {
	switch msg := msg.(type) {
	case remoteBrowserLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.files = msg.files
		m.currentDir = msg.dir
		m.cursor = 0
		m.err = ""
		m.searchMode = false
		m.searchQuery = ""
		m.searchFiles = nil
		m.filterFiles()
		return m, nil

	case remoteBrowserSearchMsg:
		// Only process if this is for the current query (ignore stale results)
		if msg.query != m.searchQuery {
			return m, nil
		}
		m.loading = false
		m.searchTriggered = true
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.searchFiles = msg.files
		m.cursor = 0
		m.err = ""
		return m, nil

	case searchDebounceMsg:
		// Only search if query hasn't changed since debounce was scheduled
		if msg.query == m.searchQuery && len(m.searchQuery) >= 3 && !m.searchTriggered {
			m.loading = true
			m.pendingSearch = ""
			return m, m.runSearch()
		}
		return m, nil

	case tea.KeyMsg:
		// Allow navigation even while loading
		if m.loading && m.searchMode {
			switch msg.String() {
			case "esc":
				m.searchMode = false
				m.searchQuery = ""
				m.searchFiles = nil
				m.pendingSearch = ""
				m.searchTriggered = false
				m.loading = false
				m.cursor = 0
				return m, nil
			case "up", "ctrl+p":
				if m.cursor > 0 {
					m.cursor--
				}
				return m, nil
			case "down", "ctrl+n":
				if m.cursor < len(m.searchFiles)-1 {
					m.cursor++
				}
				return m, nil
			}
			return m, nil
		}

		if m.loading {
			return m, nil
		}

		// Handle search mode input
		if m.searchMode {
			switch msg.String() {
			case "esc":
				// Exit search mode
				m.searchMode = false
				m.searchQuery = ""
				m.searchFiles = nil
				m.pendingSearch = ""
				m.searchTriggered = false
				m.cursor = 0
				return m, nil

			case "enter":
				if len(m.searchFiles) > 0 && m.cursor < len(m.searchFiles) {
					// Select from search results
					file := m.searchFiles[m.cursor]
					if file.IsDir {
						// Navigate to directory
						m.searchMode = false
						m.searchQuery = ""
						m.searchFiles = nil
						m.pendingSearch = ""
						m.searchTriggered = false
						m.loading = true
						return m, m.loadDirectory(file.Path)
					} else if m.mode == BrowseFiles {
						// Select file
						if m.session != nil {
							m.session.Close()
						}
						return m, func() tea.Msg {
							return remoteBrowserResultMsg{path: file.Path, selected: true}
						}
					}
				}
				return m, nil

			case "up", "ctrl+p":
				if m.cursor > 0 {
					m.cursor--
				}
				return m, nil

			case "down", "ctrl+n":
				if m.cursor < len(m.searchFiles)-1 {
					m.cursor++
				}
				return m, nil

			case "backspace":
				if len(m.searchQuery) > 0 {
					m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
					m.searchTriggered = false
					if len(m.searchQuery) < 3 {
						m.searchFiles = nil
						m.pendingSearch = ""
					} else {
						// Schedule debounced search
						m.pendingSearch = m.searchQuery
						return m, m.scheduleSearch(m.searchQuery)
					}
				}
				return m, nil

			default:
				// Add character to search query
				char := msg.String()
				if len(char) == 1 && char[0] >= 32 && char[0] < 127 {
					m.searchQuery += char
					m.searchTriggered = false
					if len(m.searchQuery) >= 3 {
						// Schedule debounced search
						m.pendingSearch = m.searchQuery
						return m, m.scheduleSearch(m.searchQuery)
					}
				}
				return m, nil
			}
		}

		// Normal mode
		switch msg.String() {
		case "q", "ctrl+c":
			// Cancel
			if m.session != nil {
				m.session.Close()
			}
			return m, func() tea.Msg {
				return remoteBrowserResultMsg{selected: false}
			}

		case "esc":
			// Cancel or exit search
			if m.searchMode {
				m.searchMode = false
				m.searchQuery = ""
				m.searchFiles = nil
				return m, nil
			}
			if m.session != nil {
				m.session.Close()
			}
			return m, func() tea.Msg {
				return remoteBrowserResultMsg{selected: false}
			}

		case "/":
			// Enter search mode
			m.searchMode = true
			m.searchQuery = ""
			m.searchFiles = nil
			m.cursor = 0
			return m, nil

		case ".":
			// Toggle hidden files
			m.showHidden = !m.showHidden
			m.filterFiles()
			// Adjust cursor if it's now out of bounds
			if m.cursor >= len(m.visibleFiles) {
				m.cursor = len(m.visibleFiles) - 1
				if m.cursor < 0 {
					m.cursor = 0
				}
			}
			return m, nil

		case "enter":
			if len(m.visibleFiles) == 0 {
				return m, nil
			}

			file := m.visibleFiles[m.cursor]

			if file.IsDir {
				if m.mode == BrowseDirectories {
					// If browsing for directories and selected a dir, offer to select it
					// For now, enter the directory; 's' will select current dir
					m.loading = true
					return m, m.loadDirectory(file.Path)
				}
				// Enter directory
				m.loading = true
				return m, m.loadDirectory(file.Path)
			} else {
				// File selected
				if m.mode == BrowseFiles {
					if m.session != nil {
						m.session.Close()
					}
					return m, func() tea.Msg {
						return remoteBrowserResultMsg{path: file.Path, selected: true}
					}
				}
			}

		case "s", " ":
			// Select current directory (for BrowseDirectories mode)
			if m.mode == BrowseDirectories {
				path := m.currentDir
				// If in search mode and on a directory, select that
				if m.searchMode && len(m.searchFiles) > 0 && m.searchFiles[m.cursor].IsDir {
					path = m.searchFiles[m.cursor].Path
				}
				if m.session != nil {
					m.session.Close()
				}
				return m, func() tea.Msg {
					return remoteBrowserResultMsg{path: path, selected: true}
				}
			}

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil

		case "down", "j":
			if m.cursor < len(m.visibleFiles)-1 {
				m.cursor++
			}
			return m, nil

		case "home", "g":
			m.cursor = 0
			return m, nil

		case "end", "G":
			if len(m.visibleFiles) > 0 {
				m.cursor = len(m.visibleFiles) - 1
			}
			return m, nil

		case "backspace", "h", "left":
			// Go to parent directory
			parent := filepath.Dir(m.currentDir)
			if parent != m.currentDir {
				m.loading = true
				return m, m.loadDirectory(parent)
			}

		case "~":
			// Go to home directory
			m.loading = true
			return m, m.loadDirectory("~")

		case "right", "l":
			// Enter directory if on one
			if len(m.visibleFiles) > 0 && m.visibleFiles[m.cursor].IsDir {
				m.loading = true
				return m, m.loadDirectory(m.visibleFiles[m.cursor].Path)
			}
		}
	}

	return m, nil
}

func (m *remoteBrowserModel) View() string {
	var sections []string

	// Title
	title := fmt.Sprintf("📂 Remote Browser: %s", m.host)
	sections = append(sections, m.styles.Header.Render(title))

	// Current path or search mode indicator
	pathStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	if m.searchMode {
		searchStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
		cursor := "_"
		if m.loading {
			cursor = ""
		}
		searchPrompt := fmt.Sprintf("  🔍 Search: %s%s", m.searchQuery, cursor)
		if len(m.searchQuery) < 3 {
			searchPrompt = fmt.Sprintf("  🔍 Search: %s%s (type %d more)", m.searchQuery, cursor, 3-len(m.searchQuery))
		}
		sections = append(sections, searchStyle.Render(searchPrompt))
		sections = append(sections, m.styles.HelpText.Render("  in: "+m.currentDir))
	} else {
		sections = append(sections, pathStyle.Render("  "+m.currentDir))
	}
	sections = append(sections, "")

	// Error message
	if m.err != "" {
		sections = append(sections, m.styles.Error.Render("Error: "+m.err))
		sections = append(sections, "")
	}

	// Loading indicator
	if m.loading {
		loadingStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
		if m.searchMode {
			sections = append(sections, loadingStyle.Render("  Searching..."))
		} else {
			sections = append(sections, loadingStyle.Render("  Loading..."))
		}
	} else {
		// Choose which file list to display
		displayFiles := m.visibleFiles
		if m.searchMode && len(m.searchFiles) > 0 {
			displayFiles = m.searchFiles
		} else if m.searchMode && len(m.searchQuery) >= 3 && m.searchTriggered && len(m.searchFiles) == 0 {
			// No results (only show after search completed)
			noResultsStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Italic(true)
			sections = append(sections, noResultsStyle.Render("  No files found matching '"+m.searchQuery+"'"))
			displayFiles = nil
		} else if m.searchMode {
			displayFiles = nil
		}

		if displayFiles != nil {
			// File list
			visibleHeight := m.height - 12 // Account for header, footer, etc.
			if visibleHeight < 5 {
				visibleHeight = 5
			}

			// Calculate scroll window
			start := 0
			if m.cursor >= visibleHeight {
				start = m.cursor - visibleHeight + 1
			}
			end := start + visibleHeight
			if end > len(displayFiles) {
				end = len(displayFiles)
			}

			for i := start; i < end; i++ {
				file := displayFiles[i]
				// In search mode, show full path instead of just name
				if m.searchMode {
					line := m.renderSearchResultLine(file, i == m.cursor)
					sections = append(sections, line)
				} else {
					line := m.renderFileLine(file, i == m.cursor)
					sections = append(sections, line)
				}
			}

			// Show scroll indicator if needed
			if len(displayFiles) > visibleHeight {
				scrollInfo := fmt.Sprintf("  [%d/%d]", m.cursor+1, len(displayFiles))
				sections = append(sections, m.styles.HelpText.Render(scrollInfo))
			}
		}
	}

	sections = append(sections, "")

	// Hidden files indicator
	if !m.searchMode {
		hiddenStatus := "hidden: off"
		if m.showHidden {
			hiddenStatus = "hidden: on"
		}
		sections = append(sections, m.styles.HelpText.Render("  ["+hiddenStatus+"]"))
	}

	// Help text
	var helpText string
	if m.searchMode {
		helpText = " Type to search | ↑/↓: navigate | Enter: select | Esc: cancel search"
	} else if m.mode == BrowseDirectories {
		helpText = " ↑/↓: navigate | Enter: open | s: select | .: toggle hidden | Esc: cancel"
	} else {
		helpText = " ↑/↓: navigate | Enter: select | /: search | .: toggle hidden | Esc: cancel"
	}
	sections = append(sections, m.styles.HelpText.Render(helpText))

	content := lipgloss.JoinVertical(lipgloss.Left, sections...)

	return lipgloss.Place(
		m.width,
		m.height,
		lipgloss.Center,
		lipgloss.Center,
		m.styles.FormContainer.Render(content),
	)
}

func (m *remoteBrowserModel) renderFileLine(file transfer.RemoteFile, selected bool) string {
	icon := "📄"
	if file.IsDir {
		icon = "📁"
	}
	if file.Name == ".." {
		icon = "⬆️"
	}

	name := file.Name
	if file.IsDir && file.Name != ".." {
		name = name + "/"
	}

	// Truncate long names
	maxLen := m.width - 20
	if maxLen < 20 {
		maxLen = 20
	}
	if len(name) > maxLen {
		name = name[:maxLen-3] + "..."
	}

	// Size display for files
	sizeStr := ""
	if !file.IsDir && file.Size > 0 {
		sizeStr = formatSize(file.Size)
	}

	line := fmt.Sprintf("  %s %s", icon, name)
	if sizeStr != "" {
		padding := maxLen - len(name) + 2
		if padding < 1 {
			padding = 1
		}
		line += strings.Repeat(" ", padding) + sizeStr
	}

	if selected {
		return m.styles.Selected.Render(line)
	}

	if file.IsDir {
		return m.styles.DirStyle.Render(line)
	}

	return line
}

// renderSearchResultLine renders a search result showing the full path
func (m *remoteBrowserModel) renderSearchResultLine(file transfer.RemoteFile, selected bool) string {
	icon := "📄"
	if file.IsDir {
		icon = "📁"
	}

	// Show the full path for search results
	displayPath := file.Path
	if file.IsDir {
		displayPath = displayPath + "/"
	}

	// Truncate long paths from the beginning
	maxLen := m.width - 10
	if maxLen < 30 {
		maxLen = 30
	}
	if len(displayPath) > maxLen {
		displayPath = "..." + displayPath[len(displayPath)-maxLen+3:]
	}

	line := fmt.Sprintf("  %s %s", icon, displayPath)

	if selected {
		return m.styles.Selected.Render(line)
	}

	if file.IsDir {
		return m.styles.DirStyle.Render(line)
	}

	return line
}

func formatSize(size int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case size >= GB:
		return fmt.Sprintf("%.1fG", float64(size)/float64(GB))
	case size >= MB:
		return fmt.Sprintf("%.1fM", float64(size)/float64(MB))
	case size >= KB:
		return fmt.Sprintf("%.1fK", float64(size)/float64(KB))
	default:
		return fmt.Sprintf("%dB", size)
	}
}

// Standalone browser for CLI use

type standaloneRemoteBrowser struct {
	*remoteBrowserModel
}

func (m standaloneRemoteBrowser) Init() tea.Cmd {
	return m.remoteBrowserModel.Init()
}

func (m standaloneRemoteBrowser) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.remoteBrowserModel.width = msg.Width
		m.remoteBrowserModel.height = msg.Height
		m.remoteBrowserModel.styles = NewStyles(msg.Width)
		return m, nil

	case remoteBrowserResultMsg:
		// Store result for retrieval
		if msg.selected {
			m.remoteBrowserModel.selected = msg.path
		}
		return m, tea.Quit
	}

	newModel, cmd := m.remoteBrowserModel.Update(msg)
	m.remoteBrowserModel = newModel
	return m, cmd
}

func (m standaloneRemoteBrowser) View() string {
	return m.remoteBrowserModel.View()
}

// RunRemoteBrowser runs the remote browser as a standalone TUI and returns the selected path
func RunRemoteBrowser(host, startPath, configFile string, mode BrowserMode) (string, bool, error) {
	styles := NewStyles(80)
	browser := NewRemoteBrowser(host, startPath, configFile, mode, styles, 80, 24)
	m := standaloneRemoteBrowser{browser}

	p := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return "", false, err
	}

	if result, ok := finalModel.(standaloneRemoteBrowser); ok {
		if result.remoteBrowserModel.selected != "" {
			return result.remoteBrowserModel.selected, true, nil
		}
	}

	return "", false, nil
}
