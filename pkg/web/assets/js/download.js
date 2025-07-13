// Download page functionality
class DownloadManager {
    constructor(downloadFolder) {
        this.downloadFolder = downloadFolder;
        this.refs = {
            downloadForm: document.getElementById('downloadForm'),
            magnetURI: document.getElementById('magnetURI'),
            torrentFiles: document.getElementById('torrentFiles'),
            arr: document.getElementById('arr'),
            downloadAction: document.getElementById('downloadAction'),
            downloadUncached: document.getElementById('downloadUncached'),
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
        this.refs.downloadFolder.addEventListener('change', () => this.saveOptions());

        // File input enhancement
        this.refs.torrentFiles.addEventListener('change', (e) => this.handleFileSelection(e));

        // Drag and drop
        this.setupDragAndDrop();
    }

    loadSavedOptions() {
        const savedOptions = {
            category: localStorage.getItem('downloadCategory') || '',
            action: localStorage.getItem('downloadAction') || 'symlink',
            uncached: localStorage.getItem('downloadUncached') === 'true',
            folder: localStorage.getItem('downloadFolder') || this.downloadFolder
        };

        this.refs.arr.value = savedOptions.category;
        this.refs.downloadAction.value = savedOptions.action;
        this.refs.downloadUncached.checked = savedOptions.uncached;
        this.refs.downloadFolder.value = savedOptions.folder;
    }

    saveOptions() {
        localStorage.setItem('downloadCategory', this.refs.arr.value);
        localStorage.setItem('downloadAction', this.refs.downloadAction.value);
        localStorage.setItem('downloadUncached', this.refs.downloadUncached.checked.toString());
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

        // Get URLs
        const urls = this.refs.magnetURI.value
            .split('\n')
            .map(url => url.trim())
            .filter(url => url.length > 0);

        if (urls.length > 0) {
            formData.append('urls', urls.join('\n'));
        }

        // Get files
        for (let i = 0; i < this.refs.torrentFiles.files.length; i++) {
            formData.append('files', this.refs.torrentFiles.files[i]);
        }

        // Validation
        const totalItems = urls.length + this.refs.torrentFiles.files.length;
        if (totalItems === 0) {
            window.decypharrUtils.createToast('Please provide at least one torrent', 'warning');
            return;
        }

        if (totalItems > 100) {
            window.decypharrUtils.createToast('Please submit up to 100 torrents at a time', 'warning');
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

            const response = await window.decypharrUtils.fetcher('/api/add', {
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
                if (result.results.length > 0) {
                    window.decypharrUtils.createToast(
                        `Added ${result.results.length} torrents with ${result.errors.length} errors`,
                        'warning'
                    );
                    this.showErrorDetails(result.errors);
                } else {
                    window.decypharrUtils.createToast('Failed to add torrents', 'error');
                    this.showErrorDetails(result.errors);
                }
            } else {
                window.decypharrUtils.createToast(
                    `Successfully added ${result.results.length} torrent${result.results.length > 1 ? 's' : ''}!`
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

    showErrorDetails(errors) {
        // Create a modal or detailed view for errors
        const errorList = errors.map(error => `• ${error}`).join('\n');
        console.error('Download errors:', errorList);

        // You could also show this in a modal for better UX
        setTimeout(() => {
            if (confirm('Some torrents failed to add. Would you like to see the details?')) {
                alert(errorList);
            }
        }, 1000);
    }

    clearForm() {
        this.refs.magnetURI.value = '';
        this.refs.torrentFiles.value = '';
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
    }
}