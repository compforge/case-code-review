(function () {
    function pretty(value) { return typeof value === 'string' ? value : JSON.stringify(value, null, 2); }
    function renderCall(container, call) {
        function message(text) {
            const p = document.createElement('p');
            p.className = 'section-note';
            p.textContent = text;
            container.append(p);
        }
        function section(title, value, missing) {
            const heading = document.createElement('h4');
            heading.textContent = title;
            container.append(heading);
            if (value === undefined) { message(missing); return; }
            const pre = document.createElement('pre');
            pre.className = 'stage-call-content';
            pre.textContent = pretty(value);
            container.append(pre);
        }
        function recordedJSON(title, value) {
            if (value === undefined) return;
            const details = document.createElement('details');
            const summary = document.createElement('summary');
            summary.textContent = title;
            const pre = document.createElement('pre');
            pre.className = 'stage-call-content';
            pre.textContent = pretty(value);
            details.append(summary, pre);
            container.append(details);
        }
        function modelMessage(label, value) {
            if (!value.content || value.content.length === 0) { section(label, ''); return; }
            value.content.forEach(function (block) {
                if (block.type === 'text') section(label, block.text);
                else if (block.type === 'thinking') section(label + ' · Reasoning', block.thinking);
                else section(label + ' · ' + block.type, block);
            });
        }
        if (call.kind === 'model') {
            const usage = call.response && call.response.usage;
            if (usage) {
                const summary = document.createElement('div');
                summary.className = 'model-call-usage';
                [['Model', usage.model], ['Input tokens', usage.input], ['Output tokens', usage.output],
                 ['Cache read', usage.cache_read], ['Cache write', usage.cache_write], ['Total tokens', usage.total_tokens],
                 ['Cost (USD)', usage.cost ? usage.cost.total : undefined]].forEach(function (entry) {
                    const item = document.createElement('span');
                    item.textContent = entry[0] + ': ' + (entry[1] === undefined || entry[1] === '' ? 'not recorded' : entry[1]);
                    summary.append(item);
                });
                container.append(summary);
            } else { message('Token usage and cost not recorded in Session.'); }
            if (call.error) section('Call error', call.error);
            if (call.request) {
                (call.request.messages || []).forEach(function (item) { modelMessage('Prompt · ' + item.role, item); });
                recordedJSON('Request tools', call.request.tools);
            } else { message('Prompt not recorded in Session.'); }
            if (call.response) modelMessage('Response', call.response);
            else message('Response not recorded in Session.');
            recordedJSON('Full Request', call.request);
            recordedJSON('Full Response', call.response);
        } else {
            if (call.tool) message('Tool: ' + call.tool);
            section('Input', call.input_recorded ? call.input : undefined, 'Input not recorded in Session.');
            section('Output · Session tool result', call.output ? call.output.content : undefined, call.output ? 'No text output recorded in Session.' : 'Output not recorded in Session.');
            if (call.output && call.output.content_blocks) section('Content blocks', call.output.content_blocks);
            if (call.output && call.output.details != null) section('Result details', call.output.details);
            if (call.is_error || (call.output && call.output.is_error)) message('Tool reported an error.');
        }
        if (call.kind !== 'model' && call.error) section('Call error', call.error);
        (call.warnings || []).forEach(message);
    }
    function element(tag, text, className) {
        const node = document.createElement(tag);
        if (text !== undefined) node.textContent = text;
        if (className) node.className = className;
        return node;
    }
    function renderAgent(container, agent, loadCall) {
        container.append(element('p', 'Model node height shows relative total tokens within this loop (minimum height retained). Width follows tree depth. Unknown usage stays unknown; Turn and Tool nodes have compact height.', 'section-note'));
        const browser = element('div', undefined, 'agent-browser');
        const tree = element('nav', undefined, 'agent-tree');
        tree.setAttribute('aria-label', 'Agent execution tree');
        const detail = element('section', undefined, 'agent-node-detail');
        detail.setAttribute('aria-label', 'Selected node details');
        browser.append(tree, detail);
        container.append(browser);
        const items = (agent.turns || []).flatMap(turn => turn.items).concat(agent.operations || []);
        const maxTokens = items.reduce((max, item) => Math.max(max, item.usage?.total_tokens || 0), 1);
        let selected = null;
        let selectedButton = null;
        let pending = null;
        let loaded = false;
        let callContainer = null;
        function deactivate() {
            if (pending) pending.abort();
            pending = null;
        }
        async function activate() {
            if (!selected || loaded || pending || !callContainer) return;
            const controller = new AbortController();
            pending = controller;
            const target = callContainer;
            target.textContent = 'Loading call details…';
            try {
                const call = await loadCall(selected.stage_id, controller.signal);
                if (controller.signal.aborted) return;
                target.replaceChildren();
                renderCall(target, call);
                loaded = true;
            } catch (error) {
                if (!controller.signal.aborted) {
                    target.replaceChildren(element('p', 'Unable to load call: ' + error.message, 'timeline-notice error'));
                    const retry = element('button', 'Retry');
                    retry.type = 'button';
                    retry.addEventListener('click', activate);
                    target.append(retry);
                }
            } finally {
                if (pending === controller) pending = null;
            }
        }
        function select(item, button) {
            deactivate();
            selected = item;
            loaded = false;
            callContainer = null;
            if (selectedButton) selectedButton.removeAttribute('aria-current');
            selectedButton = button;
            button.setAttribute('aria-current', 'true');
            detail.replaceChildren(element('h4', item.label));
            const seconds = ms => (ms / 1000).toFixed(1) + 's';
            const timing = item.offset_ms === undefined ? '' : ' · Start ' + seconds(item.offset_ms) + ' · ' + (item.duration_ms === undefined ? 'end not recorded' : seconds(item.duration_ms));
            detail.append(element('p', item.status + timing, 'agent-call-meta'));
            if (item.error) detail.append(element('p', item.error, 'timeline-notice error'));
            const open = element('button', 'Open Default view', 'agent-default-link');
            open.type = 'button';
            open.dataset.timelineStage = item.stage_id;
            detail.append(open);
            if (item.kind === 'model' || item.kind === 'tool') {
                callContainer = element('div', undefined, 'stage-call');
                detail.append(callContainer);
                activate();
            } else {
                detail.append(element('p', 'Select a child node to inspect its call. Full stage attributes are available in Default view.', 'section-note'));
            }
            detail.scrollTop = 0;
        }
        function node(item, parent, depth) {
            const branch = element('details', undefined, 'agent-branch');
            branch.open = true;
            const summary = element('summary');
            summary.style.paddingLeft = (.4 + depth * .85) + 'rem';
            const button = element('button', undefined, 'agent-node agent-node-' + item.kind);
            button.type = 'button';
            button.append(element('strong', item.label + (item.attempt ? ' · attempt ' + item.attempt : '')));
            button.append(element('span', item.status, 'agent-node-status'));
            if (item.kind === 'model') {
                const tokens = item.usage?.total_tokens;
                // +spec: Only a model call's recorded usage controls height; parent
                // summaries and cache components never add a second token charge.
                button.style.minHeight = (48 + 96 * (tokens === undefined ? 0 : tokens / maxTokens)) + 'px';
                button.append(element('span', tokens === undefined ? 'Tokens not recorded' : tokens.toLocaleString() + ' tokens', 'agent-node-tokens'));
            }
            button.addEventListener('click', event => { event.preventDefault(); select(item, button); });
            summary.append(button);
            branch.append(summary);
            parent.append(branch);
            if (!selected) select(item, button);
            return branch;
        }
        function children(list, parent, baseDepth) {
            const stack = [];
            list.forEach(item => {
                while (stack.length && stack[stack.length - 1].depth >= item.depth) stack.pop();
                const owner = stack.length ? stack[stack.length - 1].branch : parent;
                const branch = node(item, owner, baseDepth + item.depth);
                stack.push({depth: item.depth, branch});
            });
        }
        (agent.turns || []).forEach(turn => {
            const parent = node({...turn, kind: 'turn'}, tree, 0);
            children(turn.items, parent, 0);
        });
        if (agent.operations?.length) {
            tree.append(element('h4', 'Outside recorded turns'));
            children(agent.operations, tree, -1);
        }
        tree.querySelectorAll('.agent-branch').forEach(branch => {
            if (!branch.querySelector('.agent-branch')) branch.classList.add('agent-leaf');
        });
        if (!selected) detail.append(element('p', 'No turns or operations recorded.', 'section-note'));
        if (agent.warnings?.length) {
            const diagnostics = element('details', undefined, 'recording-warning');
            diagnostics.append(element('summary', agent.warnings.length + ' recording diagnostics'));
            agent.warnings.forEach(warning => diagnostics.append(element('p', warning)));
            container.append(diagnostics);
        }
        return {activate, deactivate};
    }
    window.CCRStageViews = {renderCall, renderAgent};
}());
