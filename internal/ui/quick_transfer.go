package ui

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Gu1llaum-3/sshm/internal/history"
	"github.com/Gu1llaum-3/sshm/internal/transfer"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// QuickTransferState represents the current state of the quick transfer flow
type QuickTransferState int

const (
	QTStateChooseDirection QuickTransferState = iota
	QTStateSelectingLocal
	QTStateSelectingRemote
	QTStateTransferring
	QTStateDone
)

// quickTransferModel is a streamlined transfer UI
type quickTransferModel struct {
	state            QuickTransferState
	direction        transfer.Direction
	selectedIdx      int // 0 = upload, 1 = download (for arrow key nav)
	hostName         string
	configFile       string
	localPath        string
	remotePath       string
	styles           Styles
	width            int
	height           int
	err              string
	historyManager   *history.HistoryManager
}

// quickTransferDoneMsg signals transfer complete
type quickTransferDoneMsg struct {
	success bool
	err     error
}

// quickTransferCancelMsg signals cancellation
type quickTransferCancelMsg struct{}

// quickLocalPickedMsg is sent when local file is picked
type quickLocalPickedMsg struct {
	path     string
	selected bool
}

// quickRemotePickedMsg is sent when remote file is picked
type quickRemotePickedMsg struct {
	path     string
	selected bool
}

// openRemoteBrowserMsg requests the main app to open the remote browser
type openRemoteBrowserMsg struct {
	host       string
	startPath  string
	configFile string
	mode       BrowserMode
}

// NewQuickTransfer creates a new quick transfer model
func NewQuickTransfer(hostName string, styles Styles, width, height int, configFile string) *quickTransferModel {
	historyManager, _ := history.NewHistoryManager()
	return &quickTransferModel{
		state:          QTStateChooseDirection,
		hostName:       hostName,
		configFile:     configFile,
		styles:         styles,
		width:          width,
		height:         height,
		historyManager: historyManager,
	}
}

func (m *quickTransferModel) Init() tea.Cmd {
	return nil
}

func (m *quickTransferModel) Update(msg tea.Msg) (*quickTransferModel, tea.Cmd) {
	switch msg := msg.(type) {
	case quickLocalPickedMsg:
		if !msg.selected {
			// Cancelled - go back or exit
			return m, func() tea.Msg { return quickTransferCancelMsg{} }
		}
		m.localPath = msg.path

		if m.direction == transfer.Download {
			// For downloads: both paths set (remote first, then local), execute transfer
			m.state = QTStateTransferring
			return m, m.executeTransfer()
		}
		// For uploads: local picked, now ask for remote destination
		m.state = QTStateSelectingRemote
		return m, m.openRemotePicker()

	case quickRemotePickedMsg:
		if !msg.selected {
			// Cancelled - go back or exit
			return m, func() tea.Msg { return quickTransferCancelMsg{} }
		}
		m.remotePath = msg.path

		if m.direction == transfer.Download {
			// For downloads: remote picked, now ask for local destination
			m.state = QTStateSelectingLocal
			return m, m.openLocalPicker()
		}
		// For uploads: both paths set, execute transfer
		m.state = QTStateTransferring
		return m, m.executeTransfer()

	case quickTransferDoneMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			m.state = QTStateDone
			return m, nil
		}
		m.state = QTStateDone
		return m, func() tea.Msg { return quickTransferCancelMsg{} }

	case tea.KeyMsg:
		switch m.state {
		case QTStateChooseDirection:
			switch msg.String() {
			case "u", "U", "1":
				m.direction = transfer.Upload
				m.state = QTStateSelectingLocal
				return m, m.openLocalPicker()
			case "d", "D", "2":
				m.direction = transfer.Download
				m.state = QTStateSelectingRemote
				return m, m.openRemotePicker()
			case "left", "h", "up", "k":
				m.selectedIdx = 0 // Upload
				return m, nil
			case "right", "l", "down", "j":
				m.selectedIdx = 1 // Download
				return m, nil
			case "tab":
				m.selectedIdx = (m.selectedIdx + 1) % 2
				return m, nil
			case "enter", " ":
				if m.selectedIdx == 0 {
					m.direction = transfer.Upload
					m.state = QTStateSelectingLocal
					return m, m.openLocalPicker()
				} else {
					m.direction = transfer.Download
					m.state = QTStateSelectingRemote
					return m, m.openRemotePicker()
				}
			case "esc", "q", "ctrl+c":
				return m, func() tea.Msg { return quickTransferCancelMsg{} }
			}

		case QTStateDone:
			// Any key exits
			return m, func() tea.Msg { return quickTransferCancelMsg{} }
		}
	}

	return m, nil
}

func (m *quickTransferModel) openLocalPicker() tea.Cmd {
	return func() tea.Msg {
		var mode transfer.PickerMode
		var title string

		if m.direction == transfer.Upload {
			mode = transfer.PickFile
			title = "Select file to upload"
		} else {
			mode = transfer.PickDirectory
			title = "Select download destination"
		}

		startDir, _ := os.Getwd()
		result, err := transfer.OpenFilePicker(mode, title, startDir)
		if err != nil || result == nil || !result.Selected {
			return quickLocalPickedMsg{selected: false}
		}

		return quickLocalPickedMsg{path: result.Path, selected: true}
	}
}

func (m *quickTransferModel) openRemotePicker() tea.Cmd {
	// Send a message to the main app to open the remote browser
	// This avoids nested tea.Program issues
	var mode BrowserMode
	if m.direction == transfer.Upload {
		mode = BrowseDirectories
	} else {
		mode = BrowseFiles
	}

	return func() tea.Msg {
		return openRemoteBrowserMsg{
			host:       m.hostName,
			startPath:  "~",
			configFile: m.configFile,
			mode:       mode,
		}
	}
}

func (m *quickTransferModel) executeTransfer() tea.Cmd {
	return func() tea.Msg {
		localPath := m.localPath
		recursive := false

		if m.direction == transfer.Upload {
			// Check if uploading a directory
			info, err := os.Stat(localPath)
			if err == nil && info.IsDir() {
				recursive = true
			}
		} else {
			// Download: if local path is a directory, append the remote filename
			info, err := os.Stat(localPath)
			if err == nil && info.IsDir() {
				remoteFilename := filepath.Base(m.remotePath)
				localPath = filepath.Join(localPath, remoteFilename)
			}
		}

		req := &transfer.TransferRequest{
			Host:       m.hostName,
			Direction:  m.direction,
			LocalPath:  localPath,
			RemotePath: m.remotePath,
			Recursive:  recursive,
			ConfigFile: m.configFile,
		}

		result := req.ExecuteWithProgress()
		if !result.Success {
			return quickTransferDoneMsg{success: false, err: result.Error}
		}

		// Record in history
		if m.historyManager != nil {
			direction := "upload"
			if m.direction == transfer.Download {
				direction = "download"
			}
			_ = m.historyManager.RecordTransfer(m.hostName, direction, m.localPath, m.remotePath)
		}

		return quickTransferDoneMsg{success: true}
	}
}

func (m *quickTransferModel) View() string {
	var sections []string

	// Title
	title := m.styles.Header.Render("📁 Quick Transfer")
	sections = append(sections, title)
	sections = append(sections, m.styles.HelpText.Render(fmt.Sprintf("Host: %s", m.hostName)))
	sections = append(sections, "")

	if m.err != "" {
		sections = append(sections, m.styles.Error.Render("Error: "+m.err))
		sections = append(sections, "")
		sections = append(sections, m.styles.HelpText.Render("Press any key to close"))
	} else {
		switch m.state {
		case QTStateChooseDirection:
			sections = append(sections, m.styles.Label.Render("What would you like to do?"))
			sections = append(sections, "")

			var uploadBtn, downloadBtn string
			if m.selectedIdx == 0 {
				uploadBtn = m.styles.ActiveTab.Render("  ↑ Upload  ")
				downloadBtn = m.styles.InactiveTab.Render("  ↓ Download  ")
			} else {
				uploadBtn = m.styles.InactiveTab.Render("  ↑ Upload  ")
				downloadBtn = m.styles.ActiveTab.Render("  ↓ Download  ")
			}
			buttons := lipgloss.JoinHorizontal(lipgloss.Center, uploadBtn, "    ", downloadBtn)
			sections = append(sections, buttons)
			sections = append(sections, "")
			sections = append(sections, m.styles.HelpText.Render("←/→ or Tab: switch • Enter: confirm • Esc: cancel"))

		case QTStateSelectingLocal:
			if m.direction == transfer.Upload {
				sections = append(sections, m.styles.Label.Render("Select file to upload..."))
			} else {
				sections = append(sections, m.styles.Label.Render("Select download destination..."))
			}
			sections = append(sections, "")
			loadingStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
			sections = append(sections, loadingStyle.Render("Opening file picker..."))

		case QTStateSelectingRemote:
			if m.direction == transfer.Upload {
				sections = append(sections, m.styles.Label.Render("Select remote destination..."))
			} else {
				sections = append(sections, m.styles.Label.Render("Select remote file to download..."))
			}
			sections = append(sections, "")
			if m.localPath != "" {
				sections = append(sections, m.styles.HelpText.Render("Local: "+m.localPath))
				sections = append(sections, "")
			}
			loadingStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
			sections = append(sections, loadingStyle.Render("Opening remote browser..."))

		case QTStateTransferring:
			direction := "Uploading"
			if m.direction == transfer.Download {
				direction = "Downloading"
			}
			sections = append(sections, m.styles.Label.Render(direction+"..."))
			sections = append(sections, "")
			sections = append(sections, m.styles.HelpText.Render("Local: "+m.localPath))
			sections = append(sections, m.styles.HelpText.Render("Remote: "+m.remotePath))
			sections = append(sections, "")
			loadingStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
			sections = append(sections, loadingStyle.Render("Transfer in progress..."))

		case QTStateDone:
			sections = append(sections, m.styles.Label.Render("✓ Transfer complete!"))
			sections = append(sections, "")
			sections = append(sections, m.styles.HelpText.Render("Local: "+m.localPath))
			sections = append(sections, m.styles.HelpText.Render("Remote: "+m.remotePath))
		}
	}

	content := lipgloss.JoinVertical(lipgloss.Left, sections...)

	return lipgloss.Place(
		m.width,
		m.height,
		lipgloss.Center,
		lipgloss.Center,
		m.styles.FormContainer.Render(content),
	)
}

// Standalone wrapper
type standaloneQuickTransfer struct {
	*quickTransferModel
}

func (m standaloneQuickTransfer) Init() tea.Cmd {
	return m.quickTransferModel.Init()
}

func (m standaloneQuickTransfer) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.quickTransferModel.width = msg.Width
		m.quickTransferModel.height = msg.Height
		m.quickTransferModel.styles = NewStyles(msg.Width)
		return m, nil

	case quickTransferCancelMsg:
		return m, tea.Quit

	case openRemoteBrowserMsg:
		// Standalone mode: launch remote browser as external program
		return m, func() tea.Msg {
			path, selected, err := RunRemoteBrowser(msg.host, msg.startPath, msg.configFile, msg.mode)
			if err != nil || !selected {
				return quickRemotePickedMsg{selected: false}
			}
			return quickRemotePickedMsg{path: path, selected: true}
		}
	}

	newModel, cmd := m.quickTransferModel.Update(msg)
	m.quickTransferModel = newModel
	return m, cmd
}

func (m standaloneQuickTransfer) View() string {
	return m.quickTransferModel.View()
}

// RunQuickTransfer runs the quick transfer UI
func RunQuickTransfer(hostName, configFile string) error {
	styles := NewStyles(80)
	qt := NewQuickTransfer(hostName, styles, 80, 24, configFile)
	m := standaloneQuickTransfer{qt}

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
