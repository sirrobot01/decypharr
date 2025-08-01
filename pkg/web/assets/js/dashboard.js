// Dashboard functionality for torrent and NZB management
class Dashboard {
    constructor() {
        this.state = {
            mode: 'torrents', // 'torrents' or 'nzbs'
            torrents: [],
            nzbs: [],
            selectedItems: new Set(),
            categories: new Set(),
            filteredItems: [],
            selectedCategory: '',
            selectedState: '',
            sortBy: 'added_on',
            itemsPerPage: 20,
            currentPage: 1,
            selectedItemContextMenu: null
        };

        this.refs = {
            // Mode switching
            torrentsMode: document.getElementById('torrentsMode'),
            nzbsMode: document.getElementById('nzbsMode'),
            
            // Table elements
            dataList: document.getElementById('dataList'),
            torrentsHeaders: document.getElementById('torrentsHeaders'),
            nzbsHeaders: document.getElementById('nzbsHeaders'),
            
            // Controls
            categoryFilter: document.getElementById('categoryFilter'),
            stateFilter: document.getElementById('stateFilter'),
            sortSelector: document.getElementById('sortSelector'),
            selectAll: document.getElementById('selectAll'),
            selectAllNzb: document.getElementById('selectAllNzb'),
            batchDeleteBtn: document.getElementById('batchDeleteBtn'),
            batchDeleteDebridBtn: document.getElementById('batchDeleteDebridBtn'),
            refreshBtn: document.getElementById('refreshBtn'),
            
            // Context menus
            torrentContextMenu: document.getElementById('torrentContextMenu'),
            nzbContextMenu: document.getElementById('nzbContextMenu'),
            
            // Pagination and empty state
            paginationControls: document.getElementById('paginationControls'),
            paginationInfo: document.getElementById('paginationInfo'),
            emptyState: document.getElementById('emptyState'),
            emptyStateTitle: document.getElementById('emptyStateTitle'),
            emptyStateMessage: document.getElementById('emptyStateMessage')
        };

        this.init();
    }

    init() {
        this.bindEvents();
        this.loadModeFromURL();
        this.loadData();
        this.startAutoRefresh();
    }

    bindEvents() {
        // Mode switching
        this.refs.torrentsMode.addEventListener('click', () => this.switchMode('torrents'));
        this.refs.nzbsMode.addEventListener('click', () => this.switchMode('nzbs'));

        // Refresh button
        this.refs.refreshBtn.addEventListener('click', () => this.loadData());

        // Batch delete
        this.refs.batchDeleteBtn.addEventListener('click', () => this.deleteSelectedItems());
        this.refs.batchDeleteDebridBtn.addEventListener('click', () => this.deleteSelectedItems(true));

        // Select all checkboxes
        this.refs.selectAll.addEventListener('change', (e) => this.toggleSelectAll(e.target.checked));
        this.refs.selectAllNzb.addEventListener('change', (e) => this.toggleSelectAll(e.target.checked));

        // Filters
        this.refs.categoryFilter.addEventListener('change', (e) => this.setFilter('category', e.target.value));
        this.refs.stateFilter.addEventListener('change', (e) => this.setFilter('state', e.target.value));
        this.refs.sortSelector.addEventListener('change', (e) => this.setSort(e.target.value));

        // Context menu
        this.bindContextMenu();

        // Item selection
        this.refs.dataList.addEventListener('change', (e) => {
            if (e.target.classList.contains('item-select')) {
                this.toggleItemSelection(e.target.dataset.id, e.target.checked);
            }
        });
    }

    switchMode(mode) {
        if (this.state.mode === mode) return;
        
        this.state.mode = mode;
        this.state.selectedItems.clear();
        
        // Update URL parameter
        this.updateURL(mode);
        
        // Update button states
        if (mode === 'torrents') {
            this.refs.torrentsMode.classList.remove('btn-outline');
            this.refs.torrentsMode.classList.add('btn-primary');
            this.refs.nzbsMode.classList.remove('btn-primary');
            this.refs.nzbsMode.classList.add('btn-outline');
            
            // Show torrent headers, hide NZB headers
            this.refs.torrentsHeaders.classList.remove('hidden');
            this.refs.nzbsHeaders.classList.add('hidden');
            
            // Update empty state
            this.refs.emptyStateTitle.textContent = 'No Torrents Found';
            this.refs.emptyStateMessage.textContent = "You haven't added any torrents yet. Start by adding your first download!";
            
            // Show debrid batch delete button
            this.refs.batchDeleteDebridBtn.classList.remove('hidden');
        } else {
            this.refs.nzbsMode.classList.remove('btn-outline');
            this.refs.nzbsMode.classList.add('btn-primary');
            this.refs.torrentsMode.classList.remove('btn-primary');
            this.refs.torrentsMode.classList.add('btn-outline');
            
            // Show NZB headers, hide torrent headers
            this.refs.nzbsHeaders.classList.remove('hidden');
            this.refs.torrentsHeaders.classList.add('hidden');
            
            // Update empty state
            this.refs.emptyStateTitle.textContent = 'No NZBs Found';
            this.refs.emptyStateMessage.textContent = "You haven't added any NZB downloads yet. Start by adding your first NZB!";
            
            // Hide debrid batch delete button (not relevant for NZBs)
            this.refs.batchDeleteDebridBtn.classList.add('hidden');
        }
        
        // Reset filters and reload data
        this.state.selectedCategory = '';
        this.state.selectedState = '';
        this.state.currentPage = 1;
        this.refs.categoryFilter.value = '';
        this.refs.stateFilter.value = '';
        
        this.loadData();
        this.updateBatchActions();
    }

    updateBatchActions() {
        const hasSelection = this.state.selectedItems.size > 0;
        
        // Show/hide batch delete button
        if (this.refs.batchDeleteBtn) {
            this.refs.batchDeleteBtn.classList.toggle('hidden', !hasSelection);
        }
        
        // Show/hide debrid batch delete button (only for torrents)
        if (this.refs.batchDeleteDebridBtn) {
            const showDebridButton = hasSelection && this.state.mode === 'torrents';
            this.refs.batchDeleteDebridBtn.classList.toggle('hidden', !showDebridButton);
        }
        
        // Update button text with count
        if (hasSelection) {
            const count = this.state.selectedItems.size;
            const itemType = this.state.mode === 'torrents' ? 'Torrent' : 'NZB';
            const itemTypePlural = this.state.mode === 'torrents' ? 'Torrents' : 'NZBs';
            
            if (this.refs.batchDeleteBtn) {
                const deleteText = count === 1 ? `Delete ${itemType}` : `Delete ${count} ${itemTypePlural}`;
                const deleteSpan = this.refs.batchDeleteBtn.querySelector('span');
                if (deleteSpan) {
                    deleteSpan.textContent = deleteText;
                }
            }
            
            if (this.refs.batchDeleteDebridBtn && this.state.mode === 'torrents') {
                const debridText = count === 1 ? 'Remove From Debrid' : `Remove ${count} From Debrid`;
                const debridSpan = this.refs.batchDeleteDebridBtn.querySelector('span');
                if (debridSpan) {
                    debridSpan.textContent = debridText;
                }
            }
        } else {
            // Reset button text when no selection
            if (this.refs.batchDeleteBtn) {
                const deleteSpan = this.refs.batchDeleteBtn.querySelector('span');
                if (deleteSpan) {
                    deleteSpan.textContent = 'Delete Selected';
                }
            }
            
            if (this.refs.batchDeleteDebridBtn) {
                const debridSpan = this.refs.batchDeleteDebridBtn.querySelector('span');
                if (debridSpan) {
                    debridSpan.textContent = 'Remove From Debrid';
                }
            }
        }
    }

    loadData() {
        if (this.state.mode === 'torrents') {
            this.loadTorrents();
        } else {
            this.loadNZBs();
        }
    }

    async loadNZBs() {
        try {
            const response = await window.decypharrUtils.fetcher('/api/nzbs');
            if (!response.ok) {
                throw new Error('Failed to fetch NZBs');
            }

            const data = await response.json();
            this.state.nzbs = data.nzbs || [];
            
            this.updateCategories();
            this.applyFilters();
            this.renderData();
        } catch (error) {
            console.error('Error loading NZBs:', error);
            window.decypharrUtils.createToast('Error loading NZBs', 'error');
        }
    }

    updateCategories() {
        const items = this.state.mode === 'torrents' ? this.state.torrents : this.state.nzbs;
        this.state.categories = new Set(items.map(item => item.category).filter(Boolean));
    }

    applyFilters() {
        if (this.state.mode === 'torrents') {
            this.filterTorrents();
        } else {
            this.filterNZBs();
        }
    }

    filterNZBs() {
        let filtered = [...this.state.nzbs];

        if (this.state.selectedCategory) {
            filtered = filtered.filter(n => n.category === this.state.selectedCategory);
        }

        if (this.state.selectedState) {
            filtered = filtered.filter(n => n.status === this.state.selectedState);
        }

        // Apply sorting
        filtered.sort((a, b) => {
            switch (this.state.sortBy) {
                case 'added_on':
                    return new Date(b.added_on) - new Date(a.added_on);
                case 'added_on_asc':
                    return new Date(a.added_on) - new Date(b.added_on);
                case 'name_asc':
                    return a.name.localeCompare(b.name);
                case 'name_desc':
                    return b.name.localeCompare(a.name);
                case 'size_desc':
                    return (b.total_size || 0) - (a.total_size || 0);
                case 'size_asc':
                    return (a.total_size || 0) - (b.total_size || 0);
                case 'progress_desc':
                    return (b.progress || 0) - (a.progress || 0);
                case 'progress_asc':
                    return (a.progress || 0) - (b.progress || 0);
                default:
                    return 0;
            }
        });

        this.state.filteredItems = filtered;
    }

    renderData() {
        if (this.state.mode === 'torrents') {
            this.renderTorrents();
        } else {
            this.renderNZBs();
        }
    }

    renderNZBs() {
        const startIndex = (this.state.currentPage - 1) * this.state.itemsPerPage;
        const endIndex = startIndex + this.state.itemsPerPage;
        const pageItems = this.state.filteredItems.slice(startIndex, endIndex);

        const tbody = this.refs.dataList;
        tbody.innerHTML = '';

        if (pageItems.length === 0) {
            this.refs.emptyState.classList.remove('hidden');
        } else {
            this.refs.emptyState.classList.add('hidden');
            pageItems.forEach(nzb => {
                const row = document.createElement('tr');
                row.className = 'hover cursor-pointer';
                row.setAttribute('data-id', nzb.id);
                row.setAttribute('data-name', nzb.name);
                row.setAttribute('data-category', nzb.category || '');

                const progressPercent = Math.round(nzb.progress || 0);
                const sizeFormatted = this.formatBytes(nzb.total_size || 0);
                const etaFormatted = this.formatETA(nzb.eta || 0);
                const ageFormatted = this.formatAge(nzb.date_posted);
                const statusBadge = this.getStatusBadge(nzb.status);

                row.innerHTML = `
                <td class="w-12">
                    <label class="cursor-pointer">
                        <input type="checkbox" class="checkbox checkbox-sm item-select" data-id="${nzb.id}">
                    </label>
                </td>
                <td class="font-medium max-w-xs">
                    <div class="truncate" title="${nzb.name}">${nzb.name}</div>
                </td>
                <td>${sizeFormatted}</td>
                <td>
                    <div class="flex items-center gap-2">
                        <div class="w-16 bg-base-300 rounded-full h-2">
                            <div class="bg-primary h-2 rounded-full transition-all duration-300" style="width: ${progressPercent}%"></div>
                        </div>
                        <span class="text-sm font-medium">${progressPercent}%</span>
                    </div>
                </td>
                <td>${etaFormatted}</td>
                <td>
                    <span class="badge badge-ghost badge-sm">${nzb.category || 'N/A'}</span>
                </td>
                <td>${statusBadge}</td>
                <td>${ageFormatted}</td>
                <td>
                    <div class="flex gap-1">
                        <button class="btn btn-ghost btn-xs" onclick="window.dashboard.deleteNZB('${nzb.id}');" title="Delete">
                            <i class="bi bi-trash"></i>
                        </button>
                    </div>
                </td>
            `;

                tbody.appendChild(row);
            });
        }

        this.updatePagination();
        this.updateSelectionUI();
    }

    getStatusBadge(status) {
        const statusMap = {
            'downloading': '<span class="badge badge-info badge-sm">Downloading</span>',
            'completed': '<span class="badge badge-success badge-sm">Completed</span>',
            'paused': '<span class="badge badge-warning badge-sm">Paused</span>',
            'failed': '<span class="badge badge-error badge-sm">Failed</span>',
            'queued': '<span class="badge badge-ghost badge-sm">Queued</span>',
            'processing': '<span class="badge badge-info badge-sm">Processing</span>',
            'verifying': '<span class="badge badge-info badge-sm">Verifying</span>',
            'repairing': '<span class="badge badge-warning badge-sm">Repairing</span>',
            'extracting': '<span class="badge badge-info badge-sm">Extracting</span>'
        };
        return statusMap[status] || '<span class="badge badge-ghost badge-sm">Unknown</span>';
    }

    formatETA(seconds) {
        if (!seconds || seconds <= 0) return 'N/A';
        
        const hours = Math.floor(seconds / 3600);
        const minutes = Math.floor((seconds % 3600) / 60);
        
        if (hours > 0) {
            return `${hours}h ${minutes}m`;
        } else {
            return `${minutes}m`;
        }
    }

    formatAge(datePosted) {
        if (!datePosted) return 'N/A';
        
        const now = new Date();
        const posted = new Date(datePosted);
        const diffMs = now - posted;
        const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));
        
        if (diffDays === 0) {
            return 'Today';
        } else if (diffDays === 1) {
            return '1 day';
        } else {
            return `${diffDays} days`;
        }
    }

    formatBytes(bytes) {
        if (!bytes || bytes === 0) return '0 B';
        
        const k = 1024;
        const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        
        return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
    }

    bindContextMenu() {
        // Show context menu
        this.refs.dataList.addEventListener('contextmenu', (e) => {
            const row = e.target.closest('tr[data-id]');
            if (!row) return;

            e.preventDefault();
            this.showContextMenu(e, row);
        });

        // Hide context menu
        document.addEventListener('click', (e) => {
            const torrentMenu = this.refs.torrentContextMenu;
            const nzbMenu = this.refs.nzbContextMenu;
            if (!torrentMenu.contains(e.target) && !nzbMenu.contains(e.target)) {
                this.hideContextMenu();
            }
        });

        // Context menu actions for torrents
        this.refs.torrentContextMenu.addEventListener('click', (e) => {
            const action = e.target.closest('[data-action]')?.dataset.action;
            if (action) {
                this.handleContextAction(action);
                this.hideContextMenu();
            }
        });

        // Context menu actions for NZBs
        this.refs.nzbContextMenu.addEventListener('click', (e) => {
            const action = e.target.closest('[data-action]')?.dataset.action;
            if (action) {
                this.handleContextAction(action);
                this.hideContextMenu();
            }
        });
    }

    showContextMenu(event, row) {
        const { pageX, pageY } = event;
        const { clientWidth, clientHeight } = document.documentElement;
        
        if (this.state.mode === 'torrents') {
            this.state.selectedItemContextMenu = {
                id: row.dataset.hash,
                name: row.dataset.name,
                category: row.dataset.category || '',
                type: 'torrent'
            };

            const menu = this.refs.torrentContextMenu;
            menu.querySelector('.torrent-name').textContent = this.state.selectedItemContextMenu.name;
            
            // Position the menu
            menu.style.left = `${Math.min(pageX, clientWidth - 200)}px`;
            menu.style.top = `${Math.min(pageY, clientHeight - 150)}px`;
            menu.classList.remove('hidden');
        } else {
            this.state.selectedItemContextMenu = {
                id: row.dataset.id,
                name: row.dataset.name,
                category: row.dataset.category || '',
                type: 'nzb'
            };

            const menu = this.refs.nzbContextMenu;
            menu.querySelector('.nzb-name').textContent = this.state.selectedItemContextMenu.name;
            
            // Position the menu
            menu.style.left = `${Math.min(pageX, clientWidth - 200)}px`;
            menu.style.top = `${Math.min(pageY, clientHeight - 150)}px`;
            menu.classList.remove('hidden');
        }
    }

    hideContextMenu() {
        this.refs.torrentContextMenu.classList.add('hidden');
        this.refs.nzbContextMenu.classList.add('hidden');
        this.state.selectedItemContextMenu = null;
    }

    async handleContextAction(action) {
        const item = this.state.selectedItemContextMenu;
        if (!item) return;

        if (item.type === 'torrent') {
            await this.handleTorrentAction(action, item);
        } else {
            await this.handleNZBAction(action, item);
        }
    }

    async handleTorrentAction(action, torrent) {

        const actions = {
            'copy-magnet': async () => {
                try {
                    await navigator.clipboard.writeText(`magnet:?xt=urn:btih:${torrent.hash}`);
                    window.decypharrUtils.createToast('Magnet link copied to clipboard');
                } catch (error) {
                    window.decypharrUtils.createToast('Failed to copy magnet link', 'error');
                }
            },
            'copy-name': async () => {
                try {
                    await navigator.clipboard.writeText(torrent.name);
                    window.decypharrUtils.createToast('Torrent name copied to clipboard');
                } catch (error) {
                    window.decypharrUtils.createToast('Failed to copy torrent name', 'error');
                }
            },
            'delete': async () => {
                await this.deleteTorrent(torrent.hash, torrent.category, false);
            }
        };

        if (actions[action]) {
            await actions[action]();
        }
    }

    async handleNZBAction(action, nzb) {
        const actions = {
            'pause': async () => {
                try {
                    const response = await window.decypharrUtils.fetcher(`/api/nzbs/${nzb.id}/pause`, {
                        method: 'POST'
                    });
                    if (response.ok) {
                        window.decypharrUtils.createToast('NZB paused successfully');
                        this.loadData();
                    } else {
                        throw new Error('Failed to pause NZB');
                    }
                } catch (error) {
                    window.decypharrUtils.createToast('Failed to pause NZB', 'error');
                }
            },
            'resume': async () => {
                try {
                    const response = await window.decypharrUtils.fetcher(`/api/nzbs/${nzb.id}/resume`, {
                        method: 'POST'
                    });
                    if (response.ok) {
                        window.decypharrUtils.createToast('NZB resumed successfully');
                        this.loadData();
                    } else {
                        throw new Error('Failed to resume NZB');
                    }
                } catch (error) {
                    window.decypharrUtils.createToast('Failed to resume NZB', 'error');
                }
            },
            'retry': async () => {
                try {
                    const response = await window.decypharrUtils.fetcher(`/api/nzbs/${nzb.id}/retry`, {
                        method: 'POST'
                    });
                    if (response.ok) {
                        window.decypharrUtils.createToast('NZB retry started successfully');
                        this.loadData();
                    } else {
                        throw new Error('Failed to retry NZB');
                    }
                } catch (error) {
                    window.decypharrUtils.createToast('Failed to retry NZB', 'error');
                }
            },
            'copy-name': async () => {
                try {
                    await navigator.clipboard.writeText(nzb.name);
                    window.decypharrUtils.createToast('NZB name copied to clipboard');
                } catch (error) {
                    window.decypharrUtils.createToast('Failed to copy NZB name', 'error');
                }
            },
            'delete': async () => {
                await this.deleteNZB(nzb.id);
            }
        };

        if (actions[action]) {
            await actions[action]();
        }
    }

    async deleteNZB(nzbId) {
        try {
            const response = await window.decypharrUtils.fetcher(`/api/nzbs/${nzbId}`, {
                method: 'DELETE'
            });
            if (response.ok) {
                window.decypharrUtils.createToast('NZB deleted successfully');
                this.loadData();
            } else {
                throw new Error('Failed to delete NZB');
            }
        } catch (error) {
            window.decypharrUtils.createToast('Failed to delete NZB', 'error');
        }
    }

    async loadTorrents() {
        try {
            // Show loading state
            this.refs.refreshBtn.disabled = true;
            this.refs.paginationInfo.textContent = 'Loading torrents...';

            const response = await window.decypharrUtils.fetcher('/api/torrents');
            if (!response.ok) throw new Error('Failed to fetch torrents');

            const torrents = await response.json();
            this.state.torrents = torrents;
            this.state.categories = new Set(torrents.map(t => t.category).filter(Boolean));

            this.updateUI();

        } catch (error) {
            console.error('Error loading torrents:', error);
            window.decypharrUtils.createToast(`Error loading torrents: ${error.message}`, 'error');
        } finally {
            this.refs.refreshBtn.disabled = false;
        }
    }

    updateUI() {
        // Apply filters based on current mode
        this.applyFilters();

        // Update category dropdown
        this.updateCategoryFilter();

        // Render data table
        this.renderData();

        // Update pagination
        this.updatePagination();

        // Update selection state
        this.updateSelectionUI();

        // Show/hide empty state
        this.toggleEmptyState();
    }

    filterTorrents() {
        let filtered = [...this.state.torrents];

        if (this.state.selectedCategory) {
            filtered = filtered.filter(t => t.category === this.state.selectedCategory);
        }

        if (this.state.selectedState) {
            filtered = filtered.filter(t => t.state?.toLowerCase() === this.state.selectedState.toLowerCase());
        }

        // Sort torrents
        filtered = this.sortTorrents(filtered);

        this.state.filteredItems = filtered;
    }

    sortTorrents(torrents) {
        const [field, direction] = this.state.sortBy.includes('_asc') || this.state.sortBy.includes('_desc')
            ? [this.state.sortBy.split('_').slice(0, -1).join('_'), this.state.sortBy.endsWith('_asc') ? 'asc' : 'desc']
            : [this.state.sortBy, 'desc'];

        return torrents.sort((a, b) => {
            let valueA, valueB;

            switch (field) {
                case 'name':
                    valueA = a.name?.toLowerCase() || '';
                    valueB = b.name?.toLowerCase() || '';
                    break;
                case 'size':
                    valueA = a.size || 0;
                    valueB = b.size || 0;
                    break;
                case 'progress':
                    valueA = a.progress || 0;
                    valueB = b.progress || 0;
                    break;
                case 'added_on':
                    valueA = a.added_on || 0;
                    valueB = b.added_on || 0;
                    break;
                default:
                    valueA = a[field] || 0;
                    valueB = b[field] || 0;
            }

            if (typeof valueA === 'string') {
                return direction === 'asc'
                    ? valueA.localeCompare(valueB)
                    : valueB.localeCompare(valueA);
            } else {
                return direction === 'asc'
                    ? valueA - valueB
                    : valueB - valueA;
            }
        });
    }

    renderTorrents() {
        const startIndex = (this.state.currentPage - 1) * this.state.itemsPerPage;
        const endIndex = Math.min(startIndex + this.state.itemsPerPage, this.state.filteredItems.length);
        const pageItems = this.state.filteredItems.slice(startIndex, endIndex);

        this.refs.dataList.innerHTML = pageItems.map(torrent => this.torrentRowTemplate(torrent)).join('');
    }

    torrentRowTemplate(torrent) {
        const progressPercent = (torrent.progress * 100).toFixed(1);
        const isSelected = this.state.selectedItems.has(torrent.hash);
        let addedOn = new Date(torrent.added_on).toLocaleString();

        return `
            <tr data-id="${torrent.hash}" 
                data-name="${this.escapeHtml(torrent.name)}" 
                data-category="${torrent.category || ''}"
                class="hover:bg-base-200 transition-colors">
                <td>
                    <label class="cursor-pointer">
                        <input type="checkbox" 
                               class="checkbox checkbox-sm item-select" 
                               data-id="${torrent.hash}" 
                               ${isSelected ? 'checked' : ''}>
                    </label>
                </td>
                <td class="max-w-xs">
                    <div class="truncate font-medium" title="${this.escapeHtml(torrent.name)}">
                        ${this.escapeHtml(torrent.name)}
                    </div>
                </td>
                <td class="text-nowrap font-mono text-sm">
                    ${window.decypharrUtils.formatBytes(torrent.size)}
                </td>
                <td class="min-w-36">
                    <div class="flex items-center gap-3">
                        <progress class="progress progress-primary w-20 h-2" 
                                  value="${progressPercent}" 
                                  max="100"></progress>
                        <span class="text-sm font-medium min-w-12">${progressPercent}%</span>
                    </div>
                </td>
                <td class="text-nowrap font-mono text-sm">
                    ${window.decypharrUtils.formatSpeed(torrent.dlspeed)}
                </td>
                <td>
                    ${torrent.category ?
            `<div class="badge badge-secondary badge-sm">${this.escapeHtml(torrent.category)}</div>` :
            '<span class="text-base-content/50">None</span>'
        }
                </td>
                <td>
                    ${torrent.debrid ?
            `<div class="badge badge-accent badge-sm">${this.escapeHtml(torrent.debrid)}</div>` :
            '<span class="text-base-content/50">None</span>'
        }
                </td>
                <td class="text-nowrap font-mono text-sm">
                    ${torrent.num_seeds || 0}
                </td>
                <td>
                    <div class="badge ${this.getStateColor(torrent.state)} badge-sm">
                        ${this.escapeHtml(torrent.state)}
                    </div>
                </td>
                <td>
                    <div class="flex gap-1">
                        <button class="btn btn-error btn-outline btn-xs tooltip" 
                                onclick="dashboard.deleteTorrent('${torrent.hash}', '${torrent.category || ''}', false);"
                                data-tip="Delete from local">
                            <i class="bi bi-trash"></i>
                        </button>
                        ${torrent.debrid && torrent.id ? `
                            <button class="btn btn-error btn-outline btn-xs tooltip" 
                                    onclick="dashboard.deleteTorrent('${torrent.hash}', '${torrent.category || ''}', true);"
                                    data-tip="Remove from ${torrent.debrid}">
                                <i class="bi bi-cloud-slash"></i>
                            </button>
                        ` : ''}
                    </div>
                </td>
            </tr>
        `;
    }

    getStateColor(state) {
        const stateColors = {
            'downloading': 'badge-primary',
            'pausedup': 'badge-success',
            'error': 'badge-error',
            'completed': 'badge-success'
        };
        return stateColors[state?.toLowerCase()] || 'badge-ghost';
    }

    updateCategoryFilter() {
        const currentCategories = Array.from(this.state.categories).sort();
        const categoryOptions = ['<option value="">All Categories</option>']
            .concat(currentCategories.map(cat =>
                `<option value="${this.escapeHtml(cat)}" ${cat === this.state.selectedCategory ? 'selected' : ''}>
                    ${this.escapeHtml(cat)}
                </option>`
            ));
        this.refs.categoryFilter.innerHTML = categoryOptions.join('');
    }

    updatePagination() {
        const totalPages = Math.ceil(this.state.filteredItems.length / this.state.itemsPerPage);
        const startIndex = (this.state.currentPage - 1) * this.state.itemsPerPage;
        const endIndex = Math.min(startIndex + this.state.itemsPerPage, this.state.filteredItems.length);

        // Update pagination info
        this.refs.paginationInfo.textContent =
            `Showing ${this.state.filteredItems.length > 0 ? startIndex + 1 : 0}-${endIndex} of ${this.state.filteredItems.length} torrents`;

        // Clear pagination controls
        this.refs.paginationControls.innerHTML = '';

        if (totalPages <= 1) return;

        // Previous button
        const prevBtn = this.createPaginationButton('❮', this.state.currentPage - 1, this.state.currentPage === 1);
        this.refs.paginationControls.appendChild(prevBtn);

        // Page numbers
        const maxPageButtons = 5;
        let startPage = Math.max(1, this.state.currentPage - Math.floor(maxPageButtons / 2));
        let endPage = Math.min(totalPages, startPage + maxPageButtons - 1);

        if (endPage - startPage + 1 < maxPageButtons) {
            startPage = Math.max(1, endPage - maxPageButtons + 1);
        }

        for (let i = startPage; i <= endPage; i++) {
            const pageBtn = this.createPaginationButton(i, i, false, i === this.state.currentPage);
            this.refs.paginationControls.appendChild(pageBtn);
        }

        // Next button
        const nextBtn = this.createPaginationButton('❯', this.state.currentPage + 1, this.state.currentPage === totalPages);
        this.refs.paginationControls.appendChild(nextBtn);
    }

    createPaginationButton(text, page, disabled = false, active = false) {
        const button = document.createElement('button');
        button.className = `join-item btn btn-sm ${active ? 'btn-active' : ''} ${disabled ? 'btn-disabled' : ''}`;
        button.textContent = text;
        button.disabled = disabled;

        if (!disabled) {
            button.addEventListener('click', () => {
                this.state.currentPage = page;
                this.updateUI();
            });
        }

        return button;
    }

    updateSelectionUI() {
        // Clean up selected torrents that no longer exist
        const currentHashes = new Set(this.state.filteredItems.map(t => t.hash));
        this.state.selectedItems.forEach(hash => {
            if (!currentHashes.has(hash)) {
                this.state.selectedItems.delete(hash);
            }
        });

        // Update batch delete button
        this.refs.batchDeleteBtn.classList.toggle('hidden', this.state.selectedItems.size === 0);
        this.refs.batchDeleteDebridBtn.classList.toggle('hidden', this.state.selectedItems.size === 0);

        // Update select all checkbox
        const visibleTorrents = this.state.filteredItems.slice(
            (this.state.currentPage - 1) * this.state.itemsPerPage,
            this.state.currentPage * this.state.itemsPerPage
        );

        this.refs.selectAll.checked = visibleTorrents.length > 0 &&
            visibleTorrents.every(torrent => this.state.selectedItems.has(torrent.hash));
        this.refs.selectAll.indeterminate = visibleTorrents.some(torrent => this.state.selectedItems.has(torrent.hash)) &&
            !visibleTorrents.every(torrent => this.state.selectedItems.has(torrent.hash));
    }

    toggleEmptyState() {
        const items = this.state.mode === 'torrents' ? this.state.torrents : this.state.nzbs;
        const isEmpty = items.length === 0;
        
        if (this.refs.emptyState) {
            this.refs.emptyState.classList.toggle('hidden', !isEmpty);
        }
        
        // Find the main data table card and toggle its visibility
        const dataTableCard = document.querySelector('.card:has(#dataList)');
        if (dataTableCard) {
            dataTableCard.classList.toggle('hidden', isEmpty);
        }
    }

    // Event handlers
    setFilter(type, value) {
        if (type === 'category') {
            this.state.selectedCategory = value;
        } else if (type === 'state') {
            this.state.selectedState = value;
        }
        this.state.currentPage = 1;
        this.updateUI();
    }

    setSort(sortBy) {
        this.state.sortBy = sortBy;
        this.state.currentPage = 1;
        this.updateUI();
    }

    toggleSelectAll(checked) {
        const visibleTorrents = this.state.filteredItems.slice(
            (this.state.currentPage - 1) * this.state.itemsPerPage,
            this.state.currentPage * this.state.itemsPerPage
        );

        visibleTorrents.forEach(torrent => {
            if (checked) {
                this.state.selectedItems.add(torrent.hash);
            } else {
                this.state.selectedItems.delete(torrent.hash);
            }
        });

        this.updateUI();
    }

    toggleItemSelection(id, checked) {
        if (checked) {
            this.state.selectedItems.add(id);
        } else {
            this.state.selectedItems.delete(id);
        }
        this.updateSelectionUI();
        this.updateBatchActions();
    }

    async deleteTorrent(hash, category, removeFromDebrid = false) {
        if (!confirm(`Are you sure you want to delete this torrent${removeFromDebrid ? ' from ' + category : ''}?`)) {
            return;
        }

        try {
            const endpoint = `/api/torrents/${encodeURIComponent(category)}/${hash}?removeFromDebrid=${removeFromDebrid}`;
            const response = await window.decypharrUtils.fetcher(endpoint, { method: 'DELETE' });

            if (!response.ok) throw new Error(await response.text());

            window.decypharrUtils.createToast('Torrent deleted successfully');
            await this.loadTorrents();

        } catch (error) {
            console.error('Error deleting torrent:', error);
            window.decypharrUtils.createToast(`Failed to delete torrent: ${error.message}`, 'error');
        }
    }

    async deleteSelectedItems(removeFromDebrid = false) {
        const count = this.state.selectedItems.size;
        if (count === 0) {
            const itemType = this.state.mode === 'torrents' ? 'torrents' : 'NZBs';
            window.decypharrUtils.createToast(`No ${itemType} selected for deletion`, 'warning');
            return;
        }
        
        const itemType = this.state.mode === 'torrents' ? 'torrent' : 'NZB';
        const itemTypePlural = this.state.mode === 'torrents' ? 'torrents' : 'NZBs';
        
        if (!confirm(`Are you sure you want to delete ${count} ${count > 1 ? itemTypePlural : itemType}${removeFromDebrid ? ' from debrid' : ''}?`)) {
            return;
        }

        try {
            if (this.state.mode === 'torrents') {
                const hashes = Array.from(this.state.selectedItems).join(',');
                const response = await window.decypharrUtils.fetcher(
                    `/api/torrents/?hashes=${encodeURIComponent(hashes)}&removeFromDebrid=${removeFromDebrid}`,
                    { method: 'DELETE' }
                );

                if (!response.ok) throw new Error(await response.text());
            } else {
                // Delete NZBs one by one
                const promises = Array.from(this.state.selectedItems).map(id => 
                    window.decypharrUtils.fetcher(`/api/nzbs/${id}`, { method: 'DELETE' })
                );
                const responses = await Promise.all(promises);
                
                for (const response of responses) {
                    if (!response.ok) throw new Error(await response.text());
                }
            }

            window.decypharrUtils.createToast(`${count} ${count > 1 ? itemTypePlural : itemType} deleted successfully`);
            this.state.selectedItems.clear();
            await this.loadData();

        } catch (error) {
            console.error(`Error deleting ${itemTypePlural}:`, error);
            window.decypharrUtils.createToast(`Failed to delete some ${itemTypePlural}: ${error.message}`, 'error');
        }
    }

    startAutoRefresh() {
        this.refreshInterval = setInterval(() => {
            this.loadData();
        }, 5000);

        // Clean up on page unload
        window.addEventListener('beforeunload', () => {
            if (this.refreshInterval) {
                clearInterval(this.refreshInterval);
            }
        });
    }

    escapeHtml(text) {
        const map = {
            '&': '&amp;',
            '<': '&lt;',
            '>': '&gt;',
            '"': '&quot;',
            "'": '&#039;'
        };
        return text ? text.replace(/[&<>"']/g, (m) => map[m]) : '';
    }

    loadModeFromURL() {
        const urlParams = new URLSearchParams(window.location.search);
        const mode = urlParams.get('mode');
        
        if (mode === 'nzbs' || mode === 'torrents') {
            this.state.mode = mode;
        } else {
            this.state.mode = 'torrents'; // Default mode
        }
        
        // Set the initial UI state without triggering reload
        this.setModeUI(this.state.mode);
    }

    setModeUI(mode) {
        if (mode === 'torrents') {
            this.refs.torrentsMode.classList.remove('btn-outline');
            this.refs.torrentsMode.classList.add('btn-primary');
            this.refs.nzbsMode.classList.remove('btn-primary');
            this.refs.nzbsMode.classList.add('btn-outline');
            
            this.refs.torrentsHeaders.classList.remove('hidden');
            this.refs.nzbsHeaders.classList.add('hidden');
            
            this.refs.emptyStateTitle.textContent = 'No Torrents Found';
            this.refs.emptyStateMessage.textContent = "You haven't added any torrents yet. Start by adding your first download!";
            
            this.refs.batchDeleteDebridBtn.classList.remove('hidden');
        } else {
            this.refs.nzbsMode.classList.remove('btn-outline');
            this.refs.nzbsMode.classList.add('btn-primary');
            this.refs.torrentsMode.classList.remove('btn-primary');
            this.refs.torrentsMode.classList.add('btn-outline');
            
            this.refs.nzbsHeaders.classList.remove('hidden');
            this.refs.torrentsHeaders.classList.add('hidden');
            
            this.refs.emptyStateTitle.textContent = 'No NZBs Found';
            this.refs.emptyStateMessage.textContent = "You haven't added any NZB downloads yet. Start by adding your first NZB!";
            
            this.refs.batchDeleteDebridBtn.classList.add('hidden');
        }
    }

    updateURL(mode) {
        const url = new URL(window.location);
        url.searchParams.set('mode', mode);
        window.history.replaceState({}, '', url);
    }
}