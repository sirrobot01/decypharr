// Dashboard functionality for torrent management
class TorrentDashboard {
    constructor() {
        this.state = {
            torrents: [],
            selectedTorrents: new Set(),
            categories: new Set(),
            filteredTorrents: [],
            selectedCategory: '',
            selectedState: '',
            sortBy: 'added_on',
            itemsPerPage: 20,
            currentPage: 1,
            selectedTorrentContextMenu: null
        };

        this.refs = {
            torrentsList: document.getElementById('torrentsList'),
            categoryFilter: document.getElementById('categoryFilter'),
            stateFilter: document.getElementById('stateFilter'),
            sortSelector: document.getElementById('sortSelector'),
            selectAll: document.getElementById('selectAll'),
            batchDeleteBtn: document.getElementById('batchDeleteBtn'),
            batchDeleteDebridBtn: document.getElementById('batchDeleteDebridBtn'),
            refreshBtn: document.getElementById('refreshBtn'),
            torrentContextMenu: document.getElementById('torrentContextMenu'),
            paginationControls: document.getElementById('paginationControls'),
            paginationInfo: document.getElementById('paginationInfo'),
            emptyState: document.getElementById('emptyState')
        };

        this.init();
    }

    init() {
        this.bindEvents();
        this.loadTorrents();
        this.startAutoRefresh();
    }

    bindEvents() {
        // Refresh button
        this.refs.refreshBtn.addEventListener('click', () => this.loadTorrents());

        // Batch delete
        this.refs.batchDeleteBtn.addEventListener('click', () => this.deleteSelectedTorrents());
        this.refs.batchDeleteDebridBtn.addEventListener('click', () => this.deleteSelectedTorrents(true));

        // Select all checkbox
        this.refs.selectAll.addEventListener('change', (e) => this.toggleSelectAll(e.target.checked));

        // Filters
        this.refs.categoryFilter.addEventListener('change', (e) => this.setFilter('category', e.target.value));
        this.refs.stateFilter.addEventListener('change', (e) => this.setFilter('state', e.target.value));
        this.refs.sortSelector.addEventListener('change', (e) => this.setSort(e.target.value));

        // Context menu
        this.bindContextMenu();

        // Torrent selection
        this.refs.torrentsList.addEventListener('change', (e) => {
            if (e.target.classList.contains('torrent-select')) {
                this.toggleTorrentSelection(e.target.dataset.hash, e.target.checked);
            }
        });
    }

    bindContextMenu() {
        // Show context menu
        this.refs.torrentsList.addEventListener('contextmenu', (e) => {
            const row = e.target.closest('tr[data-hash]');
            if (!row) return;

            e.preventDefault();
            this.showContextMenu(e, row);
        });

        // Hide context menu
        document.addEventListener('click', (e) => {
            if (!this.refs.torrentContextMenu.contains(e.target)) {
                this.hideContextMenu();
            }
        });

        // Context menu actions
        this.refs.torrentContextMenu.addEventListener('click', (e) => {
            const action = e.target.closest('[data-action]')?.dataset.action;
            if (action) {
                this.handleContextAction(action);
                this.hideContextMenu();
            }
        });
    }

    showContextMenu(event, row) {
        this.state.selectedTorrentContextMenu = {
            hash: row.dataset.hash,
            name: row.dataset.name,
            category: row.dataset.category || ''
        };

        this.refs.torrentContextMenu.querySelector('.torrent-name').textContent =
            this.state.selectedTorrentContextMenu.name;

        const { pageX, pageY } = event;
        const { clientWidth, clientHeight } = document.documentElement;
        const menu = this.refs.torrentContextMenu;

        // Position the menu
        menu.style.left = `${Math.min(pageX, clientWidth - 200)}px`;
        menu.style.top = `${Math.min(pageY, clientHeight - 150)}px`;

        menu.classList.remove('hidden');
    }

    hideContextMenu() {
        this.refs.torrentContextMenu.classList.add('hidden');
        this.state.selectedTorrentContextMenu = null;
    }

    async handleContextAction(action) {
        const torrent = this.state.selectedTorrentContextMenu;
        if (!torrent) return;

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
        // Filter torrents
        this.filterTorrents();

        // Update category dropdown
        this.updateCategoryFilter();

        // Render torrents table
        this.renderTorrents();

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

        this.state.filteredTorrents = filtered;
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
        const endIndex = Math.min(startIndex + this.state.itemsPerPage, this.state.filteredTorrents.length);
        const pageItems = this.state.filteredTorrents.slice(startIndex, endIndex);

        this.refs.torrentsList.innerHTML = pageItems.map(torrent => this.torrentRowTemplate(torrent)).join('');
    }

    torrentRowTemplate(torrent) {
        const progressPercent = (torrent.progress * 100).toFixed(1);
        const isSelected = this.state.selectedTorrents.has(torrent.hash);
        let addedOn = new Date(torrent.added_on).toLocaleString();

        return `
            <tr data-hash="${torrent.hash}" 
                data-name="${this.escapeHtml(torrent.name)}" 
                data-category="${torrent.category || ''}"
                class="hover:bg-base-200 transition-colors">
                <td>
                    <label class="cursor-pointer">
                        <input type="checkbox" 
                               class="checkbox checkbox-sm torrent-select" 
                               data-hash="${torrent.hash}" 
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
        const totalPages = Math.ceil(this.state.filteredTorrents.length / this.state.itemsPerPage);
        const startIndex = (this.state.currentPage - 1) * this.state.itemsPerPage;
        const endIndex = Math.min(startIndex + this.state.itemsPerPage, this.state.filteredTorrents.length);

        // Update pagination info
        this.refs.paginationInfo.textContent =
            `Showing ${this.state.filteredTorrents.length > 0 ? startIndex + 1 : 0}-${endIndex} of ${this.state.filteredTorrents.length} torrents`;

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
        const currentHashes = new Set(this.state.filteredTorrents.map(t => t.hash));
        this.state.selectedTorrents.forEach(hash => {
            if (!currentHashes.has(hash)) {
                this.state.selectedTorrents.delete(hash);
            }
        });

        // Update batch delete button
        this.refs.batchDeleteBtn.classList.toggle('hidden', this.state.selectedTorrents.size === 0);
        this.refs.batchDeleteDebridBtn.classList.toggle('hidden', this.state.selectedTorrents.size === 0);

        // Update select all checkbox
        const visibleTorrents = this.state.filteredTorrents.slice(
            (this.state.currentPage - 1) * this.state.itemsPerPage,
            this.state.currentPage * this.state.itemsPerPage
        );

        this.refs.selectAll.checked = visibleTorrents.length > 0 &&
            visibleTorrents.every(torrent => this.state.selectedTorrents.has(torrent.hash));
        this.refs.selectAll.indeterminate = visibleTorrents.some(torrent => this.state.selectedTorrents.has(torrent.hash)) &&
            !visibleTorrents.every(torrent => this.state.selectedTorrents.has(torrent.hash));
    }

    toggleEmptyState() {
        const isEmpty = this.state.torrents.length === 0;
        this.refs.emptyState.classList.toggle('hidden', !isEmpty);
        document.querySelector('.card:has(#torrentsList)').classList.toggle('hidden', isEmpty);
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
        const visibleTorrents = this.state.filteredTorrents.slice(
            (this.state.currentPage - 1) * this.state.itemsPerPage,
            this.state.currentPage * this.state.itemsPerPage
        );

        visibleTorrents.forEach(torrent => {
            if (checked) {
                this.state.selectedTorrents.add(torrent.hash);
            } else {
                this.state.selectedTorrents.delete(torrent.hash);
            }
        });

        this.updateUI();
    }

    toggleTorrentSelection(hash, checked) {
        if (checked) {
            this.state.selectedTorrents.add(hash);
        } else {
            this.state.selectedTorrents.delete(hash);
        }
        this.updateSelectionUI();
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

    async deleteSelectedTorrents(removeFromDebrid = false) {
        const count = this.state.selectedTorrents.size;
        if (count === 0) {
            window.decypharrUtils.createToast('No torrents selected for deletion', 'warning');
            return;
        }
        if (!confirm(`Are you sure you want to delete ${count} torrent${count > 1 ? 's' : ''}${removeFromDebrid ? ' from debrid' : ''}?`)) {
            return;
        }

        try {
            const hashes = Array.from(this.state.selectedTorrents).join(',');
            const response = await window.decypharrUtils.fetcher(
                `/api/torrents/?hashes=${encodeURIComponent(hashes)}&removeFromDebrid=${removeFromDebrid}`,
                { method: 'DELETE' }
            );

            if (!response.ok) throw new Error(await response.text());

            window.decypharrUtils.createToast(`${count} torrent${count > 1 ? 's' : ''} deleted successfully`);
            this.state.selectedTorrents.clear();
            await this.loadTorrents();

        } catch (error) {
            console.error('Error deleting torrents:', error);
            window.decypharrUtils.createToast(`Failed to delete some torrents: ${error.message}`, 'error');
        }
    }

    startAutoRefresh() {
        this.refreshInterval = setInterval(() => {
            this.loadTorrents();
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
}