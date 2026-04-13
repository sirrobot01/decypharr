// Dashboard functionality for torrent management with server-side operations
class TorrentDashboard {
    constructor() {
        this.state = {
            torrents: [],
            selectedEntries: new Set(),
            categories: [],
            total: 0,
            currentPage: 1,
            itemsPerPage: 20,
            totalPages: 0,
            searchQuery: '',
            selectedCategory: '',
            selectedState: '',
            sortBy: 'added_on',
            sortOrder: 'desc',
            selectedTorrentContextMenu: null
        };

        this.refs = {
            torrentsList: document.getElementById('torrentsList'),
            searchInput: document.getElementById('searchInput'),
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

        this.searchTimeout = null;
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

        // Search with debounce
        this.refs.searchInput.addEventListener('input', (e) => {
            clearTimeout(this.searchTimeout);
            this.searchTimeout = setTimeout(() => {
                this.state.searchQuery = e.target.value;
                this.state.currentPage = 1;
                this.loadTorrents();
            }, 300);
        });

        // Filters
        this.refs.categoryFilter.addEventListener('change', (e) => {
            this.state.selectedCategory = e.target.value;
            this.state.currentPage = 1;
            this.loadTorrents();
        });

        this.refs.stateFilter.addEventListener('change', (e) => {
            this.state.selectedState = e.target.value;
            this.state.currentPage = 1;
            this.loadTorrents();
        });

        this.refs.sortSelector.addEventListener('change', (e) => {
            const value = e.target.value;
            // Parse sort format: "field" or "field_asc" or "field_desc"
            if (value.endsWith('_asc')) {
                this.state.sortBy = value.replace('_asc', '');
                this.state.sortOrder = 'asc';
            } else if (value.endsWith('_desc')) {
                this.state.sortBy = value.replace('_desc', '');
                this.state.sortOrder = 'desc';
            } else {
                this.state.sortBy = value;
                this.state.sortOrder = 'desc';
            }
            this.state.currentPage = 1;
            this.loadTorrents();
        });

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
            },
            'delete-debrid': async () => {
                await this.deleteTorrent(torrent.hash, torrent.category, true);
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

            // Build query parameters
            const params = new URLSearchParams({
                page: this.state.currentPage,
                limit: this.state.itemsPerPage,
                sort_by: this.state.sortBy,
                sort_order: this.state.sortOrder
            });

            if (this.state.searchQuery) {
                params.set('search', this.state.searchQuery);
            }

            if (this.state.selectedCategory) {
                params.set('category', this.state.selectedCategory);
            }

            if (this.state.selectedState) {
                params.set('state', this.state.selectedState);
            }

            const response = await window.decypharrUtils.fetcher(`/api/torrents?${params}`);
            if (!response.ok) throw new Error('Failed to fetch items');

            const data = await response.json();
            this.state.torrents = data.torrents || [];
            this.state.total = data.total || 0;
            this.state.totalPages = data.total_pages || 0;
            this.state.categories = data.categories || [];

            this.updateUI();

        } catch (error) {
            console.error('Error loading items:', error);
            window.decypharrUtils.createToast(`Error loading items: ${error.message}`, 'error');
        } finally {
            this.refs.refreshBtn.disabled = false;
        }
    }

    updateUI() {
        // Update category dropdown
        this.updateCategoryFilter();

        // Render torrents table
        this.renderTorrents();

        // Update pagination
        this.renderPagination();

        // Update selection state
        this.updateSelectionUI();

        // Show/hide empty state
        this.toggleEmptyState();
    }

    updateCategoryFilter() {
        const currentValue = this.refs.categoryFilter.value;
        this.refs.categoryFilter.innerHTML = '<option value="">All Categories</option>';

        this.state.categories.forEach(category => {
            const option = document.createElement('option');
            option.value = category;
            option.textContent = category;
            if (category === currentValue) {
                option.selected = true;
            }
            this.refs.categoryFilter.appendChild(option);
        });
    }

    renderTorrents() {
        if (this.state.torrents.length === 0) {
            this.refs.torrentsList.innerHTML = '';
            return;
        }

        this.refs.torrentsList.innerHTML = this.state.torrents.map(torrent => {
            const isSelected = this.state.selectedEntries.has(torrent.info_hash);
            return `
                <tr class="hover" data-hash="${torrent.info_hash}" data-name="${this.escapeHtml(torrent.name)}" data-category="${this.escapeHtml(torrent.category || '')}">
                    <td>
                        <label class="cursor-pointer">
                            <input type="checkbox" class="checkbox checkbox-sm checkbox-primary torrent-select"
                                   data-hash="${torrent.info_hash}" ${isSelected ? 'checked' : ''}>
                        </label>
                    </td>
                    <td>
                        <div class="flex flex-col">
                            <span class="font-medium">${this.escapeHtml(torrent.name)}</span>
                            <span class="text-xs text-base-content/60 font-mono">${torrent.info_hash.substring(0, 8)}...</span>
                        </div>
                    </td>
                    <td>
                        <span class="badge badge-ghost">${this.formatSize(torrent.size)}</span>
                    </td>
                    <td>
                        ${this.renderProgressBar(torrent.progress)}
                    </td>
                    <td>
                        <span class="text-sm">${this.formatSpeed(torrent.speed ?? torrent.dlspeed)}</span>
                    </td>
                    <td>
                        ${torrent.category ? `<span class="badge badge-sm badge-outline">${this.escapeHtml(torrent.category)}</span>` : '-'}
                    </td>
                    <td>
                        ${this.renderProtocolBadge(torrent.protocol)}
                    </td>
                    <td>
                        ${torrent.debrid ? `<span class="badge badge-sm badge-primary">${this.escapeHtml(torrent.debrid)}</span>` : '-'}
                    </td>
                    <td>
                        <span class="text-sm">${torrent.num_seeds || 0}</span>
                    </td>
                    <td>
                        ${this.renderStateBadge(torrent.state)}
                    </td>
                    <td>
                        <button class="btn btn-ghost btn-xs text-error"
                                title="Delete Torrent"
                                onclick="window.dashboard.deleteTorrent('${torrent.info_hash}', '${this.escapeAttr(torrent.category || '')}', false);">
                            <i class="bi bi-trash"></i>
                        </button>
                        <button class="btn btn-ghost btn-xs text-error"
                                title="Delete from Provider"
                                onclick="window.dashboard.deleteTorrent('${torrent.info_hash}', '${this.escapeAttr(torrent.category || '')}', true);">
                            <i class="bi bi-cloud-slash"></i>
                        </button>
                    </td>
                </tr>
            `;
        }).join('');
    }

    renderProgressBar(progress) {
        const percent = Math.round(progress * 100);
        let color = 'progress-info';
        if (percent === 100) color = 'progress-success';
        else if (percent < 25) color = 'progress-error';
        else if (percent < 75) color = 'progress-warning';

        return `
            <div class="flex items-center gap-2">
                <progress class="progress ${color} w-20" value="${percent}" max="100"></progress>
                <span class="text-xs font-medium">${percent}%</span>
            </div>
        `;
    }

    renderStateBadge(state) {
        const stateMap = {
            'pausedUP': { class: 'badge-success', text: 'Completed' },
            'downloading': { class: 'badge-info', text: 'Downloading' },
            'error': { class: 'badge-error', text: 'Error' },
            'queued': { class: 'badge-ghost', text: 'Queued' },
            'paused': { class: 'badge-warning', text: 'Paused' }
        };

        const s = stateMap[state] || { class: 'badge-ghost', text: state };
        return `<span class="badge ${s.class} badge-sm">${s.text}</span>`;
    }

    renderProtocolBadge(protocol) {
        const protocolMap = {
            'torrent': { class: 'badge-accent', icon: 'bi-magnet', text: 'Torrent' },
            'nzb': { class: 'badge-secondary', icon: 'bi-newspaper', text: 'Usenet' }
        };

        const p = protocolMap[protocol] || { class: 'badge-ghost', icon: 'bi-question-circle', text: protocol || 'Unknown' };
        return `<span class="badge ${p.class} badge-sm"><i class="${p.icon} mr-1"></i>${p.text}</span>`;
    }

    renderPagination() {
        const start = (this.state.currentPage - 1) * this.state.itemsPerPage + 1;
        const end = Math.min(start + this.state.itemsPerPage - 1, this.state.total);

        this.refs.paginationInfo.textContent =
            this.state.total > 0 ?
            `Showing ${start}-${end} of ${this.state.total} items` :
            'No items found';

        if (this.state.totalPages <= 1) {
            this.refs.paginationControls.innerHTML = '';
            return;
        }

        let html = `
            <button class="join-item btn btn-sm ${this.state.currentPage === 1 ? 'btn-disabled' : ''}"
                    onclick="window.dashboard.goToPage(${this.state.currentPage - 1});">«</button>
        `;

        for (let i = 1; i <= this.state.totalPages; i++) {
            if (i === 1 || i === this.state.totalPages ||
                (i >= this.state.currentPage - 2 && i <= this.state.currentPage + 2)) {
                html += `
                    <button class="join-item btn btn-sm ${i === this.state.currentPage ? 'btn-active' : ''}"
                            onclick="window.dashboard.goToPage(${i});">${i}</button>
                `;
            } else if (i === this.state.currentPage - 3 || i === this.state.currentPage + 3) {
                html += `<button class="join-item btn btn-sm btn-disabled">...</button>`;
            }
        }

        html += `
            <button class="join-item btn btn-sm ${this.state.currentPage === this.state.totalPages ? 'btn-disabled' : ''}"
                    onclick="window.dashboard.goToPage(${this.state.currentPage + 1})">»</button>
        `;

        this.refs.paginationControls.innerHTML = html;
    }

    goToPage(page) {
        if (page < 1 || page > this.state.totalPages) return;
        this.state.currentPage = page;
        this.loadTorrents();
    }

    toggleEmptyState() {
        const hasResults = this.state.total > 0;
        this.refs.emptyState.classList.toggle('hidden', hasResults);
        this.refs.torrentsList.closest('.card').classList.toggle('hidden', !hasResults);
    }

    toggleSelectAll(checked) {
        if (checked) {
            this.state.torrents.forEach(t => this.state.selectedEntries.add(t.info_hash));
        } else {
            this.state.selectedEntries.clear();
        }
        this.renderTorrents();
        this.updateSelectionUI();
    }

    toggleTorrentSelection(hash, checked) {
        if (checked) {
            this.state.selectedEntries.add(hash);
        } else {
            this.state.selectedEntries.delete(hash);
        }
        this.updateSelectionUI();
    }

    updateSelectionUI() {
        const hasSelection = this.state.selectedEntries.size > 0;
        this.refs.batchDeleteBtn.classList.toggle('hidden', !hasSelection);
        this.refs.batchDeleteDebridBtn.classList.toggle('hidden', !hasSelection);

        const allSelected = this.state.torrents.length > 0 &&
            this.state.torrents.every(t => this.state.selectedEntries.has(t.info_hash));
        this.refs.selectAll.checked = allSelected;
    }

    async deleteTorrent(hash, category, removeFromDebrid = false) {
        if (!confirm('Are you sure you want to delete this torrent?')) return;

        try {
            const url = `${window.urlBase}api/torrents/${category}/${hash}?removeFromDebrid=${removeFromDebrid}`;
            const response = await window.decypharrUtils.fetcher(url, { method: 'DELETE' });

            if (!response.ok) throw new Error('Failed to delete entry');

            window.decypharrUtils.createToast('Item deleted successfully');
            this.state.selectedEntries.delete(hash);
            this.loadTorrents();
        } catch (error) {
            console.error('Error deleting torrent:', error);
            window.decypharrUtils.createToast('Failed to delete entry', 'error');
        }
    }

    async deleteSelectedTorrents(removeFromDebrid = false) {
        if (this.state.selectedEntries.size === 0) return;

        if (!confirm(`Delete ${this.state.selectedEntries.size} selected items?`)) return;

        try {
            const hashes = Array.from(this.state.selectedEntries).join(',');
            const url = `${window.urlBase}api/torrents?hashes=${hashes}&removeFromDebrid=${removeFromDebrid}`;
            const response = await window.decypharrUtils.fetcher(url, { method: 'DELETE' });

            if (!response.ok) throw new Error('Failed to delete items');

            window.decypharrUtils.createToast(`Deleted ${this.state.selectedEntries.size} items successfully`);
            this.state.selectedEntries.clear();
            this.loadTorrents();
        } catch (error) {
            console.error('Error deleting items:', error);
            window.decypharrUtils.createToast('Failed to delete items', 'error');
        }
    }

    startAutoRefresh() {
        setInterval(() => {
            this.loadTorrents();
        }, 10000); // Refresh every 10 seconds
    }

    // Utility methods
    formatSize(bytes) {
        if (!bytes || bytes === 0) return '0 B';
        const k = 1024;
        const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
    }

    formatSpeed(bytesPerSec) {
        if (!bytesPerSec || bytesPerSec === 0) return '-';
        return this.formatSize(bytesPerSec) + '/s';
    }

    escapeHtml(text) {
        if (!text) return '';
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    }

    escapeAttr(text) {
        if (!text) return '';
        return text.replace(/'/g, '&#39;').replace(/"/g, '&quot;');
    }
}
