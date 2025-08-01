// Download page functionality
class DownloadManager {
    constructor(downloadFolder) {
        this.downloadFolder = downloadFolder;
        this.currentMode = 'torrent'; // Default mode
        this.refs = {
            downloadForm: document.getElementById('downloadForm'),
            // Mode controls
            torrentMode: document.getElementById('torrentMode'),
            nzbMode: document.getElementById('nzbMode'),
            // Torrent inputs
            magnetURI: document.getElementById('magnetURI'),
            torrentFiles: document.getElementById('torrentFiles'),
            torrentInputs: document.getElementById('torrentInputs'),
            // NZB inputs
            nzbURLs: document.getElementById('nzbURLs'),
            nzbFiles: document.getElementById('nzbFiles'),
            nzbInputs: document.getElementById('nzbInputs'),
            // Common form elements
            arr: document.getElementById('arr'),
            downloadAction: document.getElementById('downloadAction'),
            downloadUncached: document.getElementById('downloadUncached'),
            downloadFolder: document.getElementById('downloadFolder'),
            downloadFolderHint: document.getElementById('downloadFolderHint'),
            debrid: document.getElementById('debrid'),
            submitBtn: document.getElementById('submitDownload'),
            submitButtonText: document.getElementById('submitButtonText'),
            activeCount: document.getElementById('activeCount'),
            completedCount: document.getElementById('completedCount'),
            totalSize: document.getElementById('totalSize')
        };

        this.init();
    }

    init() {
        this.loadSavedOptions();
        this.bindEvents();
        this.handleMagnetFromURL();
        this.loadModeFromURL();
    }

    bindEvents() {
        // Form submission
        this.refs.downloadForm.addEventListener('submit', (e) => this.handleSubmit(e));

        // Mode switching
        this.refs.torrentMode.addEventListener('click', () => this.switchMode('torrent'));
        this.refs.nzbMode.addEventListener('click', () => this.switchMode('nzb'));

        // Save options on change
        this.refs.arr.addEventListener('change', () => this.saveOptions());
        this.refs.downloadAction.addEventListener('change', () => this.saveOptions());
        this.refs.downloadUncached.addEventListener('change', () => this.saveOptions());
        this.refs.downloadFolder.addEventListener('change', () => this.saveOptions());

        // File input enhancement
        this.refs.torrentFiles.addEventListener('change', (e) => this.handleFileSelection(e));
        this.refs.nzbFiles.addEventListener('change', (e) => this.handleFileSelection(e));

        // Drag and drop
        this.setupDragAndDrop();
    }

    loadSavedOptions() {
        const savedOptions = {
            category: localStorage.getItem('downloadCategory') || '',
            action: localStorage.getItem('downloadAction') || 'symlink',
            uncached: localStorage.getItem('downloadUncached') === 'true',
            folder: localStorage.getItem('downloadFolder') || this.downloadFolder,
            mode: localStorage.getItem('downloadMode') || 'torrent'
        };

        this.refs.arr.value = savedOptions.category;
        this.refs.downloadAction.value = savedOptions.action;
        this.refs.downloadUncached.checked = savedOptions.uncached;
        this.refs.downloadFolder.value = savedOptions.folder;
        this.currentMode = savedOptions.mode;
    }

    saveOptions() {
        localStorage.setItem('downloadCategory', this.refs.arr.value);
        localStorage.setItem('downloadAction', this.refs.downloadAction.value);
        localStorage.setItem('downloadUncached', this.refs.downloadUncached.checked.toString());
        localStorage.setItem('downloadFolder', this.refs.downloadFolder.value);
        localStorage.setItem('downloadMode', this.currentMode);
    }

    handleMagnetFromURL() {
        const urlParams = new URLSearchParams(window.location.search);
        const magnetURI = urlParams.get('magnet');

        if (magnetURI) {
            this.refs.magnetURI.value = magnetURI;
            history.replaceState({}, document.title, window.location.pathname);

            // Show notification
            window.decypharrUtils.createToast('Magnet link loaded from URL', 'info');
        }
    }

    async handleSubmit(e) {
        e.preventDefault();

        const formData = new FormData();
        let urls = [];
        let files = [];
        let endpoint = '/api/add';
        let itemType = 'torrent';

        if (this.currentMode === 'torrent') {
            // Get torrent URLs
            urls = this.refs.magnetURI.value
                .split('\n')
                .map(url => url.trim())
                .filter(url => url.length > 0);

            if (urls.length > 0) {
                formData.append('urls', urls.join('\n'));
            }

            // Get torrent files
            for (let i = 0; i < this.refs.torrentFiles.files.length; i++) {
                formData.append('files', this.refs.torrentFiles.files[i]);
                files.push(this.refs.torrentFiles.files[i]);
            }
        } else if (this.currentMode === 'nzb') {
            // Get NZB URLs
            urls = this.refs.nzbURLs.value
                .split('\n')
                .map(url => url.trim())
                .filter(url => url.length > 0);

            if (urls.length > 0) {
                formData.append('nzbUrls', urls.join('\n'));
            }

            // Get NZB files
            for (let i = 0; i < this.refs.nzbFiles.files.length; i++) {
                formData.append('nzbFiles', this.refs.nzbFiles.files[i]);
                files.push(this.refs.nzbFiles.files[i]);
            }

            endpoint = '/api/nzbs/add';
            itemType = 'NZB';
        }

        // Validation
        const totalItems = urls.length + files.length;
        if (totalItems === 0) {
            window.decypharrUtils.createToast(`Please provide at least one ${itemType}`, 'warning');
            return;
        }

        if (totalItems > 100) {
            window.decypharrUtils.createToast(`Please submit up to 100 ${itemType}s at a time`, 'warning');
            return;
        }

        // Add other form data
        formData.append('arr', this.refs.arr.value);
        formData.append('downloadFolder', this.refs.downloadFolder.value);
        formData.append('action', this.refs.downloadAction.value);
        formData.append('downloadUncached', this.refs.downloadUncached.checked);

        if (this.refs.debrid) {
            formData.append('debrid', this.refs.debrid.value);
        }

        try {
            // Set loading state
            window.decypharrUtils.setButtonLoading(this.refs.submitBtn, true);

            const response = await window.decypharrUtils.fetcher(endpoint, {
                method: 'POST',
                body: formData,
                headers: {} // Remove Content-Type to let browser set it for FormData
            });

            const result = await response.json();

            if (!response.ok) {
                throw new Error(result.error || 'Unknown error');
            }

            // Handle partial success
            if (result.errors && result.errors.length > 0) {
                console.log(result.errors);
                let errorMessage = ` ${result.errors.join('\n')}`;
                if (result.results.length > 0) {
                    window.decypharrUtils.createToast(
                        `Added ${result.results.length} ${itemType}s with ${result.errors.length} errors \n${errorMessage}`,
                        'warning'
                    );
                } else {
                    window.decypharrUtils.createToast(`Failed to add ${itemType}s \n${errorMessage}`, 'error');
                }
            } else {
                window.decypharrUtils.createToast(
                    `Successfully added ${result.results.length} ${itemType}${result.results.length > 1 ? 's' : ''}!`
                );
                this.clearForm();
            }

        } catch (error) {
            console.error('Error adding downloads:', error);
            window.decypharrUtils.createToast(`Error adding downloads: ${error.message}`, 'error');
        } finally {
            window.decypharrUtils.setButtonLoading(this.refs.submitBtn, false);
        }
    }

    switchMode(mode) {
        this.currentMode = mode;
        this.saveOptions();
        this.updateURL(mode);

        // Update button states
        if (mode === 'torrent') {
            this.refs.torrentMode.classList.remove('btn-outline');
            this.refs.torrentMode.classList.add('btn-primary');
            this.refs.nzbMode.classList.remove('btn-primary');
            this.refs.nzbMode.classList.add('btn-outline');

            // Show/hide sections
            this.refs.torrentInputs.classList.remove('hidden');
            this.refs.nzbInputs.classList.add('hidden');

            // Update UI text
            this.refs.submitButtonText.textContent = 'Add to Download Queue';
            this.refs.downloadFolderHint.textContent = 'Leave empty to use default qBittorrent folder';
        } else {
            this.refs.nzbMode.classList.remove('btn-outline');
            this.refs.nzbMode.classList.add('btn-primary');
            this.refs.torrentMode.classList.remove('btn-primary');
            this.refs.torrentMode.classList.add('btn-outline');

            // Show/hide sections
            this.refs.nzbInputs.classList.remove('hidden');
            this.refs.torrentInputs.classList.add('hidden');

            // Update UI text
            this.refs.submitButtonText.textContent = 'Add to NZB Queue';
            this.refs.downloadFolderHint.textContent = 'Leave empty to use default SABnzbd folder';
        }
    }

    clearForm() {
        if (this.currentMode === 'torrent') {
            this.refs.magnetURI.value = '';
            this.refs.torrentFiles.value = '';
        } else {
            this.refs.nzbURLs.value = '';
            this.refs.nzbFiles.value = '';
        }
    }

    handleFileSelection(e) {
        const files = e.target.files;
        if (files.length > 0) {
            const fileNames = Array.from(files).map(f => f.name).join(', ');
            window.decypharrUtils.createToast(
                `Selected ${files.length} file${files.length > 1 ? 's' : ''}: ${fileNames}`,
                'info'
            );
        }
    }

    setupDragAndDrop() {
        const dropZone = this.refs.downloadForm;

        ['dragenter', 'dragover', 'dragleave', 'drop'].forEach(eventName => {
            dropZone.addEventListener(eventName, this.preventDefaults, false);
        });

        ['dragenter', 'dragover'].forEach(eventName => {
            dropZone.addEventListener(eventName, () => this.highlight(dropZone), false);
        });

        ['dragleave', 'drop'].forEach(eventName => {
            dropZone.addEventListener(eventName, () => this.unhighlight(dropZone), false);
        });

        dropZone.addEventListener('drop', (e) => this.handleDrop(e), false);
    }

    preventDefaults(e) {
        e.preventDefault();
        e.stopPropagation();
    }

    highlight(element) {
        element.classList.add('border-primary', 'border-2', 'border-dashed', 'bg-primary/5');
    }

    unhighlight(element) {
        element.classList.remove('border-primary', 'border-2', 'border-dashed', 'bg-primary/5');
    }

    handleDrop(e) {
        const dt = e.dataTransfer;
        const files = dt.files;

        if (this.currentMode === 'torrent') {
            // Filter for .torrent files
            const torrentFiles = Array.from(files).filter(file =>
                file.name.toLowerCase().endsWith('.torrent')
            );

            if (torrentFiles.length > 0) {
                // Create a new FileList-like object
                const dataTransfer = new DataTransfer();
                torrentFiles.forEach(file => dataTransfer.items.add(file));
                this.refs.torrentFiles.files = dataTransfer.files;

                this.handleFileSelection({ target: { files: torrentFiles } });
            } else {
                window.decypharrUtils.createToast('Please drop .torrent files only', 'warning');
            }
        } else {
            // Filter for .nzb files
            const nzbFiles = Array.from(files).filter(file =>
                file.name.toLowerCase().endsWith('.nzb')
            );

            if (nzbFiles.length > 0) {
                // Create a new FileList-like object
                const dataTransfer = new DataTransfer();
                nzbFiles.forEach(file => dataTransfer.items.add(file));
                this.refs.nzbFiles.files = dataTransfer.files;

                this.handleFileSelection({ target: { files: nzbFiles } });
            } else {
                window.decypharrUtils.createToast('Please drop .nzb files only', 'warning');
            }
        }
    }

    loadModeFromURL() {
        const urlParams = new URLSearchParams(window.location.search);
        const mode = urlParams.get('mode');
        
        if (mode === 'nzb' || mode === 'torrent') {
            this.currentMode = mode;
        } else {
            this.currentMode = this.currentMode || 'torrent'; // Use saved preference or default
        }
        
        // Initialize the mode without updating URL again
        this.setModeUI(this.currentMode);
    }

    setModeUI(mode) {
        if (mode === 'torrent') {
            this.refs.torrentMode.classList.remove('btn-outline');
            this.refs.torrentMode.classList.add('btn-primary');
            this.refs.nzbMode.classList.remove('btn-primary');
            this.refs.nzbMode.classList.add('btn-outline');

            this.refs.torrentInputs.classList.remove('hidden');
            this.refs.nzbInputs.classList.add('hidden');

            this.refs.submitButtonText.textContent = 'Add to Download Queue';
            this.refs.downloadFolderHint.textContent = 'Leave empty to use default qBittorrent folder';
        } else {
            this.refs.nzbMode.classList.remove('btn-outline');
            this.refs.nzbMode.classList.add('btn-primary');
            this.refs.torrentMode.classList.remove('btn-primary');
            this.refs.torrentMode.classList.add('btn-outline');

            this.refs.nzbInputs.classList.remove('hidden');
            this.refs.torrentInputs.classList.add('hidden');

            this.refs.submitButtonText.textContent = 'Add to NZB Queue';
            this.refs.downloadFolderHint.textContent = 'Leave empty to use default SABnzbd folder';
        }
    }

    updateURL(mode) {
        const url = new URL(window.location);
        url.searchParams.set('mode', mode);
        window.history.replaceState({}, '', url);
    }
}