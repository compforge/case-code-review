(function () {
    document.querySelectorAll('.timeline-view').forEach(function (view) {
        const dialog = view.querySelector('.stage-dialog');
        const body = dialog.querySelector('.stage-dialog-body');
        const back = dialog.querySelector('[data-stage-back]');
        const stages = new Map(Array.from(view.querySelectorAll('template[data-stage-detail]'), template => [template.dataset.stageDetail, template]));
        let opener = null;
        let request = null;
        let current = null;
        const history = [];
        const agents = new WeakMap();
        async function fetchStage(endpoint, stageID, signal) {
            const base = location.pathname.split('/').slice(0, 4).join('/');
            const response = await fetch(base + '/' + endpoint + '?stage=' + encodeURIComponent(stageID), {signal});
            if (!response.ok) throw new Error(await response.text());
            return response.json();
        }
        function cancelLoads() {
            if (request) request.abort();
            body.querySelectorAll('[data-stage-agent]').forEach(container => agents.get(container)?.deactivate());
        }

        async function loadPanel(panel) {
            const container = panel.querySelector('[data-stage-call], [data-stage-agent]');
            if (!container) return;
            if (container.dataset.loaded) { agents.get(container)?.activate(); return; }
            const controller = new AbortController();
            request = controller;
            const isAgent = container.hasAttribute('data-stage-agent');
            const stageID = isAgent ? container.dataset.stageAgent : container.dataset.stageCall;
            container.textContent = 'Loading ' + (isAgent ? 'Agent view' : 'call details') + '…';
            try {
                const data = await fetchStage(isAgent ? 'agent' : 'call', stageID, controller.signal);
                if (controller.signal.aborted) return;
                container.replaceChildren();
                if (isAgent) agents.set(container, window.CCRStageViews.renderAgent(container, data, (id, signal) => fetchStage('call', id, signal)));
                else window.CCRStageViews.renderCall(container, data);
                container.dataset.loaded = 'true';
            } catch (error) {
                if (!controller.signal.aborted) container.textContent = 'Unable to load view: ' + error.message;
            }
        }
        function selectView(name) {
            cancelLoads();
            const panels = Array.from(body.querySelectorAll('[data-stage-panel]'));
            const selected = panels.find(panel => panel.dataset.stagePanel === name) || panels[0];
            if (!selected) return;
            current.view = selected.dataset.stagePanel;
            panels.forEach(panel => { panel.hidden = panel !== selected; });
            body.querySelectorAll('[role="tab"]').forEach(tab => {
                const active = tab.dataset.stageView === current.view;
                tab.setAttribute('aria-selected', String(active));
                tab.tabIndex = active ? 0 : -1;
            });
            loadPanel(selected);
        }
        function showStage(id, button) {
            const template = stages.get(id);
            if (!template) return;
            cancelLoads();
            if (dialog.open && current) history.push({state: current, nodes: Array.from(body.childNodes), scroll: dialog.scrollTop});
            body.replaceChildren(template.content.cloneNode(true));
            current = {id, view: button.dataset.openView || 'default'};
            selectView(current.view);
            back.hidden = history.length === 0;
            if (!dialog.open) { opener = button; dialog.showModal(); }
            dialog.scrollTop = 0;
            dialog.querySelector('[data-stage-close]').focus();
        }
        function goBack() {
            const previous = history.pop();
            if (!previous) return;
            cancelLoads();
            body.replaceChildren(...previous.nodes);
            current = previous.state;
            selectView(current.view);
            back.hidden = history.length === 0;
            dialog.scrollTop = previous.scroll;
            body.querySelector('[role="tab"][aria-selected="true"]').focus({preventScroll: true});
        }
        view.addEventListener('click', function (event) {
            const button = event.target.closest('button');
            if (!button) return;
            if (button.hasAttribute('data-timeline-stage')) { event.preventDefault(); showStage(button.dataset.timelineStage, button); }
            else if (button.hasAttribute('data-stage-view')) selectView(button.dataset.stageView);
            else if (button.hasAttribute('data-stage-back')) goBack();
            else if (button.hasAttribute('data-stage-close')) dialog.close();
        });
        body.addEventListener('keydown', function (event) {
            if (event.target.getAttribute('role') !== 'tab') return;
            const tabs = Array.from(body.querySelectorAll('[role="tab"]'));
            let index = tabs.indexOf(event.target);
            if (event.key === 'ArrowRight') index = (index + 1) % tabs.length;
            else if (event.key === 'ArrowLeft') index = (index + tabs.length - 1) % tabs.length;
            else if (event.key === 'Home') index = 0;
            else if (event.key === 'End') index = tabs.length - 1;
            else return;
            event.preventDefault(); selectView(tabs[index].dataset.stageView); tabs[index].focus();
        });
        dialog.addEventListener('click', function (event) {
            if (event.target !== dialog) return;
            const box = dialog.getBoundingClientRect();
            if (event.clientX < box.left || event.clientX > box.right || event.clientY < box.top || event.clientY > box.bottom) dialog.close();
        });
        dialog.addEventListener('close', function () {
            cancelLoads();
            history.length = 0; current = null;
            body.replaceChildren();
            if (opener && opener.isConnected) opener.focus({preventScroll: true});
        });
    });
}());
