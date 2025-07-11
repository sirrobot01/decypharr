// Repair management for Decypharr
class RepairManager {
    constructor() {
        this.state = {
            jobs: [],
            currentJob: null,
            allBrokenItems: [],
            filteredItems: [],
            selectedItems: new Set(),
            currentPage: 1,
            currentItemsPage: 1,
            itemsPerPage: 10,
            itemsPerModalPage: 20,
            searchTerm: '',
            arrFilter: '',
            pathFilter: '',
            sortBy: 'created_at',
            sortDirection: 'desc'
        };

        this.refs = {
            repairForm: document.getElementById('repairForm'),
            arrSelect: document.getElementById('arrSelect'),
            mediaIds: document.getElementById('mediaIds'),
            isAsync: document.getElementById('isAsync'),
            autoProcess: document.getElementById('autoProcess'),
            submitBtn: document.getElementById('submitRepair'),

            // Jobs table
            jobsTable: document.getElementById('jobsTable'),
            jobsTableBody: document.getElementById('jobsTableBody'),
            jobsPagination: document.getElementById('jobsPagination'),
            noJobsMessage: document.getElementById('noJobsMessage'),
            refreshJobs: document.getElementById('refreshJobs'),
            deleteSelectedJobs: document.getElementById('deleteSelectedJobs'),
            selectAllJobs: document.getElementById('selectAllJobs'),

            // Modal elements
            jobDetailsModal: document.getElementById('jobDetailsModal'),
            modalJobId: document.getElementById('modalJobId'),
            modalJobStatus: document.getElementById('modalJobStatus'),
            modalJobStarted: document.getElementById('modalJobStarted'),
            modalJobCompleted: document.getElementById('modalJobCompleted'),
            modalJobArrs: document.getElementById('modalJobArrs'),
            modalJobMediaIds: document.getElementById('modalJobMediaIds'),
            modalJobAutoProcess: document.getElementById('modalJobAutoProcess'),
            modalJobError: document.getElementById('modalJobError'),
            errorContainer: document.getElementById('errorContainer'),

            // Broken items
            brokenItemsTableBody: document.getElementById('brokenItemsTableBody'),
            itemsPagination: document.getElementById('itemsPagination'),
            noBrokenItemsMessage: document.getElementById('noBrokenItemsMessage'),
            noFilteredItemsMessage: document.getElementById('noFilteredItemsMessage'),
            totalItemsCount: document.getElementById('totalItemsCount'),
            modalFooterStats: document.getElementById('modalFooterStats'),

            // Filters
            itemSearchInput: document.getElementById('itemSearchInput'),
            arrFilterSelect: document.getElementById('arrFilterSelect'),
            pathFilterSelect: document.getElementById('pathFilterSelect'),
            clearFiltersBtn: document.getElementById('clearFiltersBtn'),

            // Action buttons
            processJobBtn: document.getElementById('processJobBtn'),
            stopJobBtn: document.getElementById('stopJobBtn')
        };

        this.init();
    }

    init() {
        this.bindEvents();
        this.loadArrInstances();
        this.loadJobs();
        this.startAutoRefresh();
    }

    bindEvents() {
        // Form submission
        this.refs.repairForm.addEventListener('submit', (e) => this.handleFormSubmit(e));

        // Jobs table events
        this.refs.refreshJobs.addEventListener('click', () => this.loadJobs());
        this.refs.deleteSelectedJobs.addEventListener('click', () => this.deleteSelectedJobs());
        this.refs.selectAllJobs.addEventListener('change', (e) => this.toggleSelectAllJobs(e.target.checked));

        // Modal events
        this.refs.processJobBtn.addEventListener('click', () => this.processCurrentJob());
        this.refs.stopJobBtn.addEventListener('click', () => this.stopCurrentJob());

        // Filter events
        this.refs.itemSearchInput.addEventListener('input',
            window.decypharrUtils.debounce(() => this.applyFilters(), 300));
        this.refs.arrFilterSelect.addEventListener('change', () => this.applyFilters());
        this.refs.pathFilterSelect.addEventListener('change', () => this.applyFilters());
        this.refs.clearFiltersBtn.addEventListener('click', () => this.clearFilters());

        // Table row events (using event delegation)
        this.refs.jobsTableBody.addEventListener('click', (e) => this.handleJobTableClick(e));
        this.refs.brokenItemsTableBody.addEventListener('click', (e) => this.handleItemTableClick(e));
    }

    async loadArrInstances() {
        try {
            const response = await window.decypharrUtils.fetcher('/api/arrs');
            if (!response.ok) throw new Error('Failed to load Arr instances');

            const arrs = await response.json();

            // Clear existing options (keep the default one)
            this.refs.arrSelect.innerHTML = '<option value="">Select an Arr instance</option>';

            arrs.forEach(arr => {
                const option = document.createElement('option');
                option.value = arr.name;
                option.textContent = `${arr.name} (${arr.host})`;
                this.refs.arrSelect.appendChild(option);
            });

        } catch (error) {
            console.error('Error loading Arr instances:', error);
            window.decypharrUtils.createToast('Failed to load Arr instances', 'error');
        }
    }

    async handleFormSubmit(e) {
        e.preventDefault();

        const arr = this.refs.arrSelect.value;
        const mediaIdsValue = this.refs.mediaIds.value.trim();

        if (!arr) {
            window.decypharrUtils.createToast('Please select an Arr instance', 'warning');
            return;
        }

        const mediaIds = mediaIdsValue ?
            mediaIdsValue.split(',').map(id => id.trim()).filter(Boolean) :
            [];

        try {
            window.decypharrUtils.setButtonLoading(this.refs.submitBtn, true);

            const response = await window.decypharrUtils.fetcher('/api/repair', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    arr: arr,
                    mediaIds: mediaIds.length > 0 ? mediaIds : null,
                    async: this.refs.isAsync.checked,
                    autoProcess: this.refs.autoProcess.checked
                })
            });

            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(errorText || 'Failed to start repair');
            }

            const result = await response.json();

            window.decypharrUtils.createToast(
                `Repair job started successfully! Job ID: ${result.job_id?.substring(0, 8) || 'Unknown'}`,
                'success'
            );

            // Clear form
            this.refs.mediaIds.value = '';

            // Refresh jobs list
            await this.loadJobs();

        } catch (error) {
            console.error('Error starting repair:', error);
            window.decypharrUtils.createToast(`Error starting repair: ${error.message}`, 'error');
        } finally {
            window.decypharrUtils.setButtonLoading(this.refs.submitBtn, false);
        }
    }

    async loadJobs() {
        try {
            const response = await window.decypharrUtils.fetcher('/api/repair/jobs');
            if (!response.ok) throw new Error('Failed to fetch jobs');

            this.state.jobs = await response.json();
            this.renderJobsTable();

        } catch (error) {
            console.error('Error loading jobs:', error);
            window.decypharrUtils.createToast('Error loading repair jobs', 'error');
        }
    }

    renderJobsTable() {
        const jobs = this.getSortedJobs();
        const totalPages = Math.ceil(jobs.length / this.state.itemsPerPage);
        const startIndex = (this.state.currentPage - 1) * this.state.itemsPerPage;
        const endIndex = Math.min(startIndex + this.state.itemsPerPage, jobs.length);
        const pageJobs = jobs.slice(startIndex, endIndex);

        // Clear table
        this.refs.jobsTableBody.innerHTML = '';
        this.refs.jobsPagination.innerHTML = '';

        // Reset selection
        this.refs.selectAllJobs.checked = false;
        this.refs.deleteSelectedJobs.disabled = true;

        if (jobs.length === 0) {
            this.refs.noJobsMessage.classList.remove('hidden');
            return;
        }

        this.refs.noJobsMessage.classList.add('hidden');

        // Render jobs
        pageJobs.forEach(job => {
            const row = this.createJobRow(job);
            this.refs.jobsTableBody.appendChild(row);
        });

        // Render pagination
        this.renderJobsPagination(totalPages);

        // Update selection state
        this.updateJobSelectionState();
    }

    createJobRow(job) {
        const row = document.createElement('tr');
        row.className = 'hover:bg-base-200 transition-colors';
        row.dataset.jobId = job.id;

        const status = this.getJobStatus(job.status);
        const startedDate = new Date(job.created_at).toLocaleString();
        const totalItems = job.broken_items ?
            Object.values(job.broken_items).reduce((sum, arr) => sum + arr.length, 0) : 0;
        const canDelete = !['started', 'processing'].includes(job.status);

        row.innerHTML = `
            <td>
                <label class="cursor-pointer">
                    <input type="checkbox" class="checkbox checkbox-sm job-checkbox" 
                           value="${job.id}" ${canDelete ? '' : 'disabled'}>
                </label>
            </td>
            <td>
                <button class="link link-primary text-sm view-job" data-job-id="${job.id}">
                    ${job.id.substring(0, 8)}...
                </button>
            </td>
            <td>
                <div class="flex flex-wrap gap-1">
                    ${job.arrs.map(arr => `<div class="badge badge-secondary badge-xs">${arr}</div>`).join('')}
                </div>
            </td>
            <td>
                <time class="text-sm" datetime="${job.created_at}">${startedDate}</time>
            </td>
            <td>
                <div class="badge ${status.class} badge-sm">${status.text}</div>
            </td>
            <td>
                <span class="font-mono text-sm">${totalItems}</span>
            </td>
            <td>
                <div class="flex gap-1">
                    ${job.status === 'pending' ? `
                        <button class="btn btn-primary btn-xs process-job" data-job-id="${job.id}">
                            <i class="bi bi-play-fill"></i>
                        </button>
                    ` : ''}
                    ${['started', 'processing'].includes(job.status) ? `
                        <button class="btn btn-warning btn-xs stop-job" data-job-id="${job.id}">
                            <i class="bi bi-stop-fill"></i>
                        </button>
                    ` : ''}
                    ${canDelete ? `
                        <button class="btn btn-error btn-xs delete-job" data-job-id="${job.id}">
                            <i class="bi bi-trash"></i>
                        </button>
                    ` : `
                        <button class="btn btn-error btn-xs" disabled>
                            <i class="bi bi-trash"></i>
                        </button>
                    `}
                </div>
            </td>
        `;

        return row;
    }

    getJobStatus(status) {
        const statusMap = {
            'pending': { text: 'Pending', class: 'badge-warning' },
            'started': { text: 'Running', class: 'badge-primary' },
            'processing': { text: 'Processing', class: 'badge-info' },
            'completed': { text: 'Completed', class: 'badge-success' },
            'failed': { text: 'Failed', class: 'badge-error' },
            'cancelled': { text: 'Cancelled', class: 'badge-ghost' }
        };

        return statusMap[status] || { text: status, class: 'badge-ghost' };
    }

    getSortedJobs() {
        const jobs = [...this.state.jobs];

        jobs.sort((a, b) => {
            let valueA, valueB;

            switch (this.state.sortBy) {
                case 'created_at':
                    valueA = new Date(a.created_at).getTime();
                    valueB = new Date(b.created_at).getTime();
                    break;
                case 'status':
                    valueA = a.status;
                    valueB = b.status;
                    break;
                case 'arrs':
                    valueA = a.arrs.join(',');
                    valueB = b.arrs.join(',');
                    break;
                default:
                    valueA = a[this.state.sortBy] || '';
                    valueB = b[this.state.sortBy] || '';
            }

            if (typeof valueA === 'string') {
                return this.state.sortDirection === 'asc' ?
                    valueA.localeCompare(valueB) : valueB.localeCompare(valueA);
            } else {
                return this.state.sortDirection === 'asc' ?
                    valueA - valueB : valueB - valueA;
            }
        });

        return jobs;
    }

    renderJobsPagination(totalPages) {
        if (totalPages <= 1) return;

        const pagination = document.createElement('div');
        pagination.className = 'join';

        // Previous button
        const prevBtn = document.createElement('button');
        prevBtn.className = `join-item btn btn-sm ${this.state.currentPage === 1 ? 'btn-disabled' : ''}`;
        prevBtn.innerHTML = '<i class="bi bi-chevron-left"></i>';
        prevBtn.disabled = this.state.currentPage === 1;
        if (this.state.currentPage > 1) {
            prevBtn.addEventListener('click', () => {
                this.state.currentPage--;
                this.renderJobsTable();
            });
        }
        pagination.appendChild(prevBtn);

        // Page numbers
        const maxPageButtons = 5;
        let startPage = Math.max(1, this.state.currentPage - Math.floor(maxPageButtons / 2));
        let endPage = Math.min(totalPages, startPage + maxPageButtons - 1);

        if (endPage - startPage + 1 < maxPageButtons) {
            startPage = Math.max(1, endPage - maxPageButtons + 1);
        }

        for (let i = startPage; i <= endPage; i++) {
            const pageBtn = document.createElement('button');
            pageBtn.className = `join-item btn btn-sm ${i === this.state.currentPage ? 'btn-active' : ''}`;
            pageBtn.textContent = i;
            pageBtn.addEventListener('click', () => {
                this.state.currentPage = i;
                this.renderJobsTable();
            });
            pagination.appendChild(pageBtn);
        }

        // Next button
        const nextBtn = document.createElement('button');
        nextBtn.className = `join-item btn btn-sm ${this.state.currentPage === totalPages ? 'btn-disabled' : ''}`;
        nextBtn.innerHTML = '<i class="bi bi-chevron-right"></i>';
        nextBtn.disabled = this.state.currentPage === totalPages;
        if (this.state.currentPage < totalPages) {
            nextBtn.addEventListener('click', () => {
                this.state.currentPage++;
                this.renderJobsTable();
            });
        }
        pagination.appendChild(nextBtn);

        this.refs.jobsPagination.appendChild(pagination);
    }

    handleJobTableClick(e) {
        const target = e.target.closest('button');
        if (!target) return;

        const jobId = target.dataset.jobId;
        if (!jobId) return;

        if (target.classList.contains('view-job')) {
            this.viewJobDetails(jobId);
        } else if (target.classList.contains('process-job')) {
            this.processJob(jobId);
        } else if (target.classList.contains('stop-job')) {
            this.stopJob(jobId);
        } else if (target.classList.contains('delete-job')) {
            this.deleteJob(jobId);
        }

        // Handle checkbox changes
        const checkbox = e.target.closest('.job-checkbox');
        if (checkbox) {
            this.updateJobSelectionState();
        }
    }

    async viewJobDetails(jobId) {
        const job = this.state.jobs.find(j => j.id === jobId);
        if (!job) return;

        this.state.currentJob = job;
        this.populateJobModal(job);
        this.refs.jobDetailsModal.showModal();
    }

    populateJobModal(job) {
        // Basic job info
        this.refs.modalJobId.textContent = job.id.substring(0, 8);
        this.refs.modalJobArrs.textContent = job.arrs.join(', ');
        this.refs.modalJobMediaIds.textContent = job.media_ids && job.media_ids.length > 0 ?
            job.media_ids.join(', ') : 'All media';
        this.refs.modalJobAutoProcess.textContent = job.auto_process ? 'Yes' : 'No';

        // Dates
        this.refs.modalJobStarted.textContent = new Date(job.created_at).toLocaleString();
        this.refs.modalJobCompleted.textContent = job.finished_at ?
            new Date(job.finished_at).toLocaleString() : 'N/A';

        // Status
        const status = this.getJobStatus(job.status);
        this.refs.modalJobStatus.innerHTML = `<span class="badge ${status.class}">${status.text}</span>`;

        // Error handling
        if (job.error) {
            this.refs.modalJobError.textContent = job.error;
            this.refs.errorContainer.classList.remove('hidden');
        } else {
            this.refs.errorContainer.classList.add('hidden');
        }

        // Action buttons
        this.refs.processJobBtn.classList.toggle('hidden', job.status !== 'pending');
        this.refs.stopJobBtn.classList.toggle('hidden', !['started', 'processing'].includes(job.status));

        // Process broken items
        if (job.broken_items) {
            this.state.allBrokenItems = this.processItemsData(job.broken_items);
            this.state.filteredItems = [...this.state.allBrokenItems];
            this.populateArrFilter();
            this.state.currentItemsPage = 1;
            this.renderBrokenItemsTable();
        } else {
            this.state.allBrokenItems = [];
            this.state.filteredItems = [];
            this.renderBrokenItemsTable();
        }

        this.updateItemsStats();
    }

    processItemsData(brokenItems) {
        const items = [];

        Object.entries(brokenItems).forEach(([arrName, itemsArray]) => {
            if (itemsArray && itemsArray.length > 0) {
                itemsArray.forEach((item, index) => {
                    items.push({
                        id: `${arrName}-${index}`,
                        arr: arrName,
                        path: item.path || item.file_path || 'Unknown path',
                        size: item.size || 0,
                        type: this.getFileType(item.path || ''),
                        fileId: item.fileId || item.id || `${arrName}-${index}`
                    });
                });
            }
        });

        return items;
    }

    getFileType(path) {
        const movieExtensions = ['.mp4', '.mkv', '.avi', '.mov', '.wmv', '.flv', '.webm'];
        const tvIndicators = ['/TV/', '/Television/', '/Series/', '/Shows/', '/tv/', '/series/'];

        const pathLower = path.toLowerCase();

        // Check for TV indicators first
        if (tvIndicators.some(indicator => pathLower.includes(indicator.toLowerCase()))) {
            return 'tv';
        }

        // Check for movie indicators
        if (movieExtensions.some(ext => pathLower.endsWith(ext))) {
            return pathLower.includes('/movies/') || pathLower.includes('/films/') ? 'movie' : 'tv';
        }

        return 'other';
    }

    populateArrFilter() {
        this.refs.arrFilterSelect.innerHTML = '<option value="">All Arrs</option>';

        const uniqueArrs = [...new Set(this.state.allBrokenItems.map(item => item.arr))];
        uniqueArrs.forEach(arr => {
            const option = document.createElement('option');
            option.value = arr;
            option.textContent = arr;
            this.refs.arrFilterSelect.appendChild(option);
        });
    }

    applyFilters() {
        const searchTerm = this.refs.itemSearchInput.value.toLowerCase();
        const arrFilter = this.refs.arrFilterSelect.value;
        const pathFilter = this.refs.pathFilterSelect.value;

        this.state.filteredItems = this.state.allBrokenItems.filter(item => {
            const matchesSearch = !searchTerm || item.path.toLowerCase().includes(searchTerm);
            const matchesArr = !arrFilter || item.arr === arrFilter;
            const matchesPath = !pathFilter || item.type === pathFilter;

            return matchesSearch && matchesArr && matchesPath;
        });

        this.state.currentItemsPage = 1;
        this.renderBrokenItemsTable();
        this.updateItemsStats();
    }

    clearFilters() {
        this.refs.itemSearchInput.value = '';
        this.refs.arrFilterSelect.value = '';
        this.refs.pathFilterSelect.value = '';
        this.applyFilters();
    }

    renderBrokenItemsTable() {
        this.refs.brokenItemsTableBody.innerHTML = '';
        this.refs.itemsPagination.innerHTML = '';

        if (this.state.allBrokenItems.length === 0) {
            this.refs.noBrokenItemsMessage.classList.remove('hidden');
            this.refs.noFilteredItemsMessage.classList.add('hidden');
            return;
        }

        if (this.state.filteredItems.length === 0) {
            this.refs.noBrokenItemsMessage.classList.add('hidden');
            this.refs.noFilteredItemsMessage.classList.remove('hidden');
            return;
        }

        this.refs.noBrokenItemsMessage.classList.add('hidden');
        this.refs.noFilteredItemsMessage.classList.add('hidden');

        // Pagination
        const totalPages = Math.ceil(this.state.filteredItems.length / this.state.itemsPerModalPage);
        const startIndex = (this.state.currentItemsPage - 1) * this.state.itemsPerModalPage;
        const endIndex = Math.min(startIndex + this.state.itemsPerModalPage, this.state.filteredItems.length);
        const pageItems = this.state.filteredItems.slice(startIndex, endIndex);

        // Render items
        pageItems.forEach(item => {
            const row = this.createBrokenItemRow(item);
            this.refs.brokenItemsTableBody.appendChild(row);
        });

        // Render pagination
        this.renderItemsPagination(totalPages);
    }

    createBrokenItemRow(item) {
        const row = document.createElement('tr');
        row.className = 'hover:bg-base-200 transition-colors cursor-pointer';
        row.dataset.itemId = item.id;

        const typeColor = {
            'movie': 'badge-primary',
            'tv': 'badge-secondary',
            'other': 'badge-ghost'
        };

        row.innerHTML = `
            <td>
                <div class="badge badge-info badge-xs">${window.decypharrUtils.escapeHtml(item.arr)}</div>
            </td>
            <td>
                <div class="text-sm max-w-xs truncate" title="${window.decypharrUtils.escapeHtml(item.path)}">
                    ${window.decypharrUtils.escapeHtml(item.path)}
                </div>
            </td>
            <td>
                <div class="badge ${typeColor[item.type]} badge-xs">${item.type}</div>
            </td>
            <td>
                <span class="text-sm font-mono">${window.decypharrUtils.formatBytes(item.size)}</span>
            </td>
        `;

        return row;
    }

    renderItemsPagination(totalPages) {
        if (totalPages <= 1) return;

        const pagination = document.createElement('div');
        pagination.className = 'join';

        // Previous button
        const prevBtn = document.createElement('button');
        prevBtn.className = `join-item btn btn-sm ${this.state.currentItemsPage === 1 ? 'btn-disabled' : ''}`;
        prevBtn.innerHTML = '<i class="bi bi-chevron-left"></i>';
        prevBtn.disabled = this.state.currentItemsPage === 1;
        if (this.state.currentItemsPage > 1) {
            prevBtn.addEventListener('click', () => {
                this.state.currentItemsPage--;
                this.renderBrokenItemsTable();
            });
        }
        pagination.appendChild(prevBtn);

        // Page numbers
        const maxButtons = 5;
        let startPage = Math.max(1, this.state.currentItemsPage - Math.floor(maxButtons / 2));
        let endPage = Math.min(totalPages, startPage + maxButtons - 1);

        for (let i = startPage; i <= endPage; i++) {
            const pageBtn = document.createElement('button');
            pageBtn.className = `join-item btn btn-sm ${i === this.state.currentItemsPage ? 'btn-active' : ''}`;
            pageBtn.textContent = i;
            pageBtn.addEventListener('click', () => {
                this.state.currentItemsPage = i;
                this.renderBrokenItemsTable();
            });
            pagination.appendChild(pageBtn);
        }

        // Next button
        const nextBtn = document.createElement('button');
        nextBtn.className = `join-item btn btn-sm ${this.state.currentItemsPage === totalPages ? 'btn-disabled' : ''}`;
        nextBtn.innerHTML = '<i class="bi bi-chevron-right"></i>';
        nextBtn.disabled = this.state.currentItemsPage === totalPages;
        if (this.state.currentItemsPage < totalPages) {
            nextBtn.addEventListener('click', () => {
                this.state.currentItemsPage++;
                this.renderBrokenItemsTable();
            });
        }
        pagination.appendChild(nextBtn);

        this.refs.itemsPagination.appendChild(pagination);
    }

    updateItemsStats() {
        this.refs.totalItemsCount.textContent = this.state.allBrokenItems.length;
        this.refs.modalFooterStats.textContent =
            `Total: ${this.state.allBrokenItems.length} | Filtered: ${this.state.filteredItems.length}`;
    }

    // Job management methods
    async processJob(jobId) {
        try {
            const response = await window.decypharrUtils.fetcher(`/api/repair/jobs/${jobId}/process`, {
                method: 'POST'
            });

            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(errorText || 'Failed to process job');
            }

            window.decypharrUtils.createToast('Job processing started', 'success');
            await this.loadJobs();

        } catch (error) {
            console.error('Error processing job:', error);
            window.decypharrUtils.createToast(`Error processing job: ${error.message}`, 'error');
        }
    }

    async stopJob(jobId) {
        if (!confirm('Are you sure you want to stop this job?')) return;

        try {
            const response = await window.decypharrUtils.fetcher(`/api/repair/jobs/${jobId}/stop`, {
                method: 'POST'
            });

            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(errorText || 'Failed to stop job');
            }

            window.decypharrUtils.createToast('Job stop requested', 'success');
            await this.loadJobs();

        } catch (error) {
            console.error('Error stopping job:', error);
            window.decypharrUtils.createToast(`Error stopping job: ${error.message}`, 'error');
        }
    }

    async deleteJob(jobId) {
        if (!confirm('Are you sure you want to delete this job?')) return;

        try {
            const response = await window.decypharrUtils.fetcher('/api/repair/jobs', {
                method: 'DELETE',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ ids: [jobId] })
            });

            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(errorText || 'Failed to delete job');
            }

            window.decypharrUtils.createToast('Job deleted successfully', 'success');
            await this.loadJobs();

        } catch (error) {
            console.error('Error deleting job:', error);
            window.decypharrUtils.createToast(`Error deleting job: ${error.message}`, 'error');
        }
    }

    async deleteSelectedJobs() {
        const selectedIds = Array.from(
            document.querySelectorAll('.job-checkbox:checked')
        ).map(checkbox => checkbox.value);

        if (selectedIds.length === 0) return;

        if (!confirm(`Are you sure you want to delete ${selectedIds.length} job(s)?`)) return;

        try {
            const response = await window.decypharrUtils.fetcher('/api/repair/jobs', {
                method: 'DELETE',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ ids: selectedIds })
            });

            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(errorText || 'Failed to delete jobs');
            }

            window.decypharrUtils.createToast(`${selectedIds.length} job(s) deleted successfully`, 'success');
            await this.loadJobs();

        } catch (error) {
            console.error('Error deleting jobs:', error);
            window.decypharrUtils.createToast(`Error deleting jobs: ${error.message}`, 'error');
        }
    }

    toggleSelectAllJobs(checked) {
        const checkboxes = document.querySelectorAll('.job-checkbox:not(:disabled)');
        checkboxes.forEach(checkbox => {
            checkbox.checked = checked;
        });
        this.updateJobSelectionState();
    }

    updateJobSelectionState() {
        const checkboxes = document.querySelectorAll('.job-checkbox');
        const checkedBoxes = document.querySelectorAll('.job-checkbox:checked');
        const enabledBoxes = document.querySelectorAll('.job-checkbox:not(:disabled)');

        // Update delete button state
        this.refs.deleteSelectedJobs.disabled = checkedBoxes.length === 0;

        // Update select all checkbox state
        if (enabledBoxes.length === 0) {
            this.refs.selectAllJobs.checked = false;
            this.refs.selectAllJobs.indeterminate = false;
        } else if (checkedBoxes.length === enabledBoxes.length) {
            this.refs.selectAllJobs.checked = true;
            this.refs.selectAllJobs.indeterminate = false;
        } else if (checkedBoxes.length > 0) {
            this.refs.selectAllJobs.checked = false;
            this.refs.selectAllJobs.indeterminate = true;
        } else {
            this.refs.selectAllJobs.checked = false;
            this.refs.selectAllJobs.indeterminate = false;
        }
    }

    // Modal action methods
    async processCurrentJob() {
        if (!this.state.currentJob) return;

        await this.processJob(this.state.currentJob.id);
        this.refs.jobDetailsModal.close();
    }

    async stopCurrentJob() {
        if (!this.state.currentJob) return;

        await this.stopJob(this.state.currentJob.id);
        this.refs.jobDetailsModal.close();
    }

    handleItemTableClick(e) {
        const row = e.target.closest('tr');
        if (!row) return;

        const itemId = row.dataset.itemId;
        if (!itemId) return;

        // Toggle selection
        if (this.state.selectedItems.has(itemId)) {
            this.state.selectedItems.delete(itemId);
            row.classList.remove('bg-primary/10');
        } else {
            this.state.selectedItems.add(itemId);
            row.classList.add('bg-primary/10');
        }
    }

    // Auto-refresh functionality
    startAutoRefresh() {
        // Refresh jobs every 10 seconds
        this.refreshInterval = setInterval(() => {
            // Only refresh if not on a modal or if there are active jobs
            const hasActiveJobs = this.state.jobs.some(job =>
                ['started', 'processing', 'pending'].includes(job.status)
            );

            if (hasActiveJobs || !this.refs.jobDetailsModal.open) {
                this.loadJobs();
            }
        }, 10000);

        // Handle page visibility changes
        document.addEventListener('visibilitychange', () => {
            if (document.hidden) {
                if (this.refreshInterval) {
                    clearInterval(this.refreshInterval);
                    this.refreshInterval = null;
                }
            } else {
                if (!this.refreshInterval) {
                    this.startAutoRefresh();
                }
            }
        });

        // Clean up on page unload
        window.addEventListener('beforeunload', () => {
            if (this.refreshInterval) {
                clearInterval(this.refreshInterval);
            }
        });
    }

    // Utility methods
    formatJobDuration(startTime, endTime) {
        if (!startTime) return 'N/A';

        const start = new Date(startTime);
        const end = endTime ? new Date(endTime) : new Date();
        const duration = Math.floor((end - start) / 1000); // seconds

        return window.decypharrUtils.formatDuration(duration);
    }

    getJobProgress(job) {
        if (!job.broken_items) return 0;

        const totalItems = Object.values(job.broken_items).reduce((sum, arr) => sum + arr.length, 0);
        if (totalItems === 0) return 100;

        // This would need to be implemented based on your API
        // For now, return based on status
        switch (job.status) {
            case 'completed': return 100;
            case 'failed': return 0;
            case 'cancelled': return 0;
            default: return 0; // Would need progress info from API
        }
    }

    // Export functionality
    async exportJobData(jobId) {
        const job = this.state.jobs.find(j => j.id === jobId);
        if (!job) return;

        const exportData = {
            job_id: job.id,
            status: job.status,
            created_at: job.created_at,
            finished_at: job.finished_at,
            arrs: job.arrs,
            media_ids: job.media_ids,
            auto_process: job.auto_process,
            broken_items: job.broken_items,
            error: job.error
        };

        try {
            const blob = new Blob([JSON.stringify(exportData, null, 2)], {
                type: 'application/json'
            });

            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = `repair-job-${job.id.substring(0, 8)}-${new Date().toISOString().split('T')[0]}.json`;
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            URL.revokeObjectURL(url);

            window.decypharrUtils.createToast('Job data exported successfully', 'success');
        } catch (error) {
            console.error('Error exporting job data:', error);
            window.decypharrUtils.createToast('Failed to export job data', 'error');
        }
    }

    // Statistics methods
    getJobStatistics() {
        const stats = {
            total: this.state.jobs.length,
            pending: 0,
            running: 0,
            completed: 0,
            failed: 0,
            cancelled: 0
        };

        this.state.jobs.forEach(job => {
            switch (job.status) {
                case 'pending':
                    stats.pending++;
                    break;
                case 'started':
                case 'processing':
                    stats.running++;
                    break;
                case 'completed':
                    stats.completed++;
                    break;
                case 'failed':
                    stats.failed++;
                    break;
                case 'cancelled':
                    stats.cancelled++;
                    break;
            }
        });

        return stats;
    }

    // Search and filter helpers
    searchJobs(searchTerm) {
        if (!searchTerm) return this.state.jobs;

        const term = searchTerm.toLowerCase();
        return this.state.jobs.filter(job =>
            job.id.toLowerCase().includes(term) ||
            job.arrs.some(arr => arr.toLowerCase().includes(term)) ||
            (job.media_ids && job.media_ids.some(id => id.toString().includes(term)))
        );
    }

    filterJobsByStatus(status) {
        if (!status) return this.state.jobs;
        return this.state.jobs.filter(job => job.status === status);
    }

    filterJobsByDate(startDate, endDate) {
        if (!startDate && !endDate) return this.state.jobs;

        return this.state.jobs.filter(job => {
            const jobDate = new Date(job.created_at);
            if (startDate && jobDate < new Date(startDate)) return false;
            if (endDate && jobDate > new Date(endDate)) return false;
            return true;
        });
    }

    // Cleanup methods
    destroy() {
        if (this.refreshInterval) {
            clearInterval(this.refreshInterval);
        }

        // Remove event listeners
        Object.values(this.refs).forEach(ref => {
            if (ref && ref.removeEventListener) {
                // Note: In a real implementation, you'd need to keep track of
                // the specific event listeners to remove them properly
            }
        });
    }
}

// Additional utility functions for repair operations
const RepairUtils = {
    // Format repair status for display
    formatRepairStatus(status, error = null) {
        const statusConfig = {
            'pending': {
                icon: 'bi-clock',
                class: 'text-warning',
                message: 'Waiting to start'
            },
            'started': {
                icon: 'bi-play-circle',
                class: 'text-primary',
                message: 'Repair in progress'
            },
            'processing': {
                icon: 'bi-gear',
                class: 'text-info',
                message: 'Processing results'
            },
            'completed': {
                icon: 'bi-check-circle',
                class: 'text-success',
                message: 'Repair completed successfully'
            },
            'failed': {
                icon: 'bi-x-circle',
                class: 'text-error',
                message: error || 'Repair failed'
            },
            'cancelled': {
                icon: 'bi-stop-circle',
                class: 'text-warning',
                message: 'Repair was cancelled'
            }
        };

        return statusConfig[status] || {
            icon: 'bi-question-circle',
            class: 'text-gray-500',
            message: `Unknown status: ${status}`
        };
    },

    // Validate media IDs input
    validateMediaIds(input) {
        if (!input || !input.trim()) return { valid: true, ids: [] };

        const ids = input.split(',').map(id => id.trim()).filter(Boolean);
        const invalidIds = ids.filter(id => !/^\d+$/.test(id));

        if (invalidIds.length > 0) {
            return {
                valid: false,
                error: `Invalid media IDs: ${invalidIds.join(', ')}. Only numeric IDs are allowed.`,
                ids: []
            };
        }

        return { valid: true, ids };
    },

    // Generate repair summary
    generateRepairSummary(job) {
        if (!job.broken_items) return 'No broken items found';

        const itemCounts = Object.entries(job.broken_items).map(([arr, items]) =>
            `${arr}: ${items.length} items`
        );

        const totalItems = Object.values(job.broken_items).reduce((sum, arr) => sum + arr.length, 0);

        return `Found ${totalItems} broken items across ${Object.keys(job.broken_items).length} Arr instance(s): ${itemCounts.join(', ')}`;
    },

    // Calculate repair completion percentage
    calculateProgress(job) {
        // This would need to be implemented based on your API
        // For now, return based on status
        switch (job.status) {
            case 'pending': return 0;
            case 'started': return 25;
            case 'processing': return 75;
            case 'completed': return 100;
            case 'failed':
            case 'cancelled': return 0;
            default: return 0;
        }
    }
};

// Initialize repair manager when DOM is ready
document.addEventListener('DOMContentLoaded', () => {
    window.repairManager = new RepairManager();
    window.RepairUtils = RepairUtils;
});

// Export for ES6 modules if needed
if (typeof module !== 'undefined' && module.exports) {
    module.exports = { RepairManager, RepairUtils };
}