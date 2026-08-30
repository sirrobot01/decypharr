// Download page functionality
class DownloadManager {
    constructor(downloadFolder) {
        this.downloadFolder = downloadFolder;
        this.refs = {
            downloadForm: document.getElementById('downloadForm'),
            magnetURI: document.getElementById('magnetURI'),
            torrentFiles: document.getElementById('torrentFiles'),
            nzbURL: document.getElementById('nzbURL'),
            nzbFile: document.getElementById('nzbFile'),
            arr: document.getElementById('arr'),
            downloadAction: document.getElementById('downloadAction'),
            downloadUncached: document.getElementById('downloadUncached'),
            rmTrackerUrls: document.getElementById('rmTrackerUrls'),
            downloadFolder: document.getElementById('downloadFolder'),
            debrid: document.getElementById('debrid'),
            submitBtn: document.getElementById('submitDownload'),
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
    }

    bindEvents() {
        // Form submission
        this.refs.downloadForm.addEventListener('submit', (e) => this.handleSubmit(e));

        // Save options on change
        this.refs.arr.addEventListener('change', () => this.saveOptions());
        this.refs.downloadAction.addEventListener('change', () => this.saveOptions());
        this.refs.downloadUncached.addEventListener('change', () => this.saveOptions());
        this.refs.rmTrackerUrls.addEventListener('change', () => this.saveOptions());
        this.refs.downloadFolder.addEventListener('change', () => this.saveOptions());

        // File input enhancement
        this.refs.torrentFiles.addEventListener('change', (e) => this.handleFileSelection(e, 'torrent'));
        this.refs.nzbFile.addEventListener('change', (e) => this.handleFileSelection(e, 'nzb'));

        // Drag and drop
        this.setupDragAndDrop();
    }

    loadSavedOptions() {
        const savedOptions = {
            category: localStorage.getItem('downloadCategory') || '',
            action: localStorage.getItem('downloadAction') || 'symlink',
            uncached: localStorage.getItem('downloadUncached') === 'true',
            rmTrackerUrls: localStorage.getItem('rmTrackerUrls') === 'true',
            folder: localStorage.getItem('downloadFolder') || this.downloadFolder
        };

        this.refs.arr.value = savedOptions.category;
        this.refs.downloadAction.value = savedOptions.action;
        this.refs.downloadUncached.checked = savedOptions.uncached;
        this.refs.rmTrackerUrls.checked = savedOptions.rmTrackerUrls;
        this.refs.downloadFolder.value = savedOptions.folder;
    }

    saveOptions() {
        localStorage.setItem('downloadCategory', this.refs.arr.value);
        localStorage.setItem('downloadAction', this.refs.downloadAction.value);
        localStorage.setItem('downloadUncached', this.refs.downloadUncached.checked.toString());

        // Only save rmTrackerUrls if not disabled (i.e., not forced by config)
        if (!this.refs.rmTrackerUrls.disabled) {
            localStorage.setItem('rmTrackerUrls', this.refs.rmTrackerUrls.checked.toString());
        }

        localStorage.setItem('downloadFolder', this.refs.downloadFolder.value);
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

        // Get URLs (torrent/magnet)
        const urls = this.refs.magnetURI.value
            .split('\n')
            .map(url => url.trim())
            .filter(url => url.length > 0);

        if (urls.length > 0) {
            formData.append('urls', urls.join('\n'));
        }

        // Get torrent files
        for (let i = 0; i < this.refs.torrentFiles.files.length; i++) {
            formData.append('files', this.refs.torrentFiles.files[i]);
        }

        // Get NZB URLs (support multiple URLs separated by newlines)
        const nzbURLs = this.refs.nzbURL.value
            .split('\n')
            .map(url => url.trim())
            .filter(url => url.length > 0);

        if (nzbURLs.length > 0) {
            formData.append('nzbURLs', nzbURLs.join('\n'));
        }

        // Get NZB files (support multiple files)
        for (let i = 0; i < this.refs.nzbFile.files.length; i++) {
            formData.append('nzbFiles', this.refs.nzbFile.files[i]);
        }

        // Validation
        const totalItems = urls.length + this.refs.torrentFiles.files.length + nzbURLs.length + this.refs.nzbFile.files.length;
        if (totalItems === 0) {
            window.decypharrUtils.createToast('Please provide at least one torrent or NZB', 'warning');
            return;
        }

        if (totalItems > 100) {
            window.decypharrUtils.createToast('Please submit up to 100 items at a time', 'warning');
            return;
        }

        // Add other form data
        formData.append('arr', this.refs.arr.value);
        formData.append('downloadFolder', this.refs.downloadFolder.value);
        formData.append('action', this.refs.downloadAction.value);
        formData.append('downloadUncached', this.refs.downloadUncached.checked);
        formData.append('rmTrackerUrls', this.refs.rmTrackerUrls.checked);

        if (this.refs.debrid) {
            formData.append('debrid', this.refs.debrid.value);
        }

        try {
            // Set loading state
            window.decypharrUtils.setButtonLoading(this.refs.submitBtn, true);

            const response = await window.decypharrUtils.fetcher('/api/add', {
                method: 'POST',
                body: formData,
                headers: {} // Remove Content-Type to let browser set it for FormData
            });

            const results = await response.json();

            if (!response.ok) {
                throw new Error(results.error || 'Unknown error');
            }

            // Separate successful and failed results
            const successes = results.filter(r => r.status !== 'error');
            const failures = results.filter(r => r.status === 'error');

            // Handle partial success
            if (failures.length > 0) {
                if (successes.length > 0) {
                    window.decypharrUtils.createToast(
                        `Added ${successes.length} item(s) with ${failures.length} error(s)`,
                        'warning'
                    );
                    this.showErrorDetails(failures);
                } else {
                    this.showErrorDetails(failures);
                }
            } else if (successes.length > 0) {
                window.decypharrUtils.createToast(
                    `Successfully added ${successes.length} item${successes.length > 1 ? 's' : ''}!`
                );
                this.clearForm();
            } else {
                window.decypharrUtils.createToast('No items were added', 'warning');
            }

        } catch (error) {
            console.error('Error adding downloads:', error);
            window.decypharrUtils.createToast(`Error adding downloads: ${error.message}`, 'error');
        } finally {
            window.decypharrUtils.setButtonLoading(this.refs.submitBtn, false);
        }
    }

    showErrorDetails(failures) {
        // Extract error messages from the failed results
        const errorList = failures.map(f => `• ${f.error || 'Unknown error'}`).join('\n');
        console.error('Download errors:', errorList);
        window.decypharrUtils.createToast(
            `Errors occurred while adding items:\n${errorList}`,
            'error'
        );
    }

    clearForm() {
        this.refs.magnetURI.value = '';
        this.refs.torrentFiles.value = '';
        this.refs.nzbURL.value = '';
        this.refs.nzbFile.value = '';
    }

    handleFileSelection(e, type) {
        const files = e.target.files;
        if (files.length > 0) {
            const fileNames = Array.from(files).map(f => f.name).join(', ');
            const fileType = type === 'nzb' ? 'NZB' : 'torrent';
            window.decypharrUtils.createToast(
                `Selected ${files.length} ${fileType} file${files.length > 1 ? 's' : ''}: ${fileNames}`,
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

        // Filter for .torrent and .nzb files
        const torrentFiles = Array.from(files).filter(file =>
            file.name.toLowerCase().endsWith('.torrent')
        );
        const nzbFiles = Array.from(files).filter(file =>
            file.name.toLowerCase().endsWith('.nzb')
        );

        if (torrentFiles.length > 0) {
            // Create a new FileList-like object for torrents
            const dataTransfer = new DataTransfer();
            torrentFiles.forEach(file => dataTransfer.items.add(file));
            this.refs.torrentFiles.files = dataTransfer.files;
            this.handleFileSelection({target: {files: torrentFiles}}, 'torrent');
        }

        if (nzbFiles.length > 0) {
            // For NZB, only take the first file since we only accept one
            const dataTransfer = new DataTransfer();
            dataTransfer.items.add(nzbFiles[0]);
            this.refs.nzbFile.files = dataTransfer.files;
            this.handleFileSelection({target: {files: [nzbFiles[0]]}}, 'nzb');
        }

        if (torrentFiles.length === 0 && nzbFiles.length === 0) {
            window.decypharrUtils.createToast('Please drop .torrent or .nzb files only', 'warning');
        }
    }
}