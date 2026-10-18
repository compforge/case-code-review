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
    function renderAgent(container, agent) {
        container.append(element('p', 'Calls are grouped by recorded turns. Start times share the loop clock; overlapping intervals can run concurrently.', 'section-note'));
        const allItems = (agent.turns || []).flatMap(turn => turn.items).concat(agent.operations || []);
        const maxTokens = Math.max(1, ...allItems.flatMap(item => item.usage ? [item.usage.input, item.usage.output] : []));
        function renderItem(item) {
            const card = element('article', undefined, 'agent-call agent-call-' + item.kind);
            card.style.paddingLeft = (1 + item.depth) + 'rem';
            const button = element('button', item.label + (item.attempt ? ' · attempt ' + item.attempt : ''), 'agent-call-title');
            button.type = 'button';
            button.dataset.timelineStage = item.stage_id;
            if (item.kind === 'model') button.dataset.openView = 'model';
            card.append(button);
            const seconds = ms => (ms / 1000).toFixed(1) + 's';
            card.append(element('p', item.status + ' · Start ' + seconds(item.offset_ms) + ' · ' + (item.duration_ms === undefined ? 'end not recorded' : seconds(item.duration_ms)), 'agent-call-meta'));
            if (item.error) card.append(element('p', item.error, 'timeline-notice error'));
            if (item.kind === 'model') {
                if (!item.usage) card.append(element('p', 'Token usage not recorded', 'section-note'));
                else {
                    // Independent input/output bars avoid adding provider-dependent
                    // cache components to an input total that may already contain them.
                    [['Input', item.usage.input], ['Output', item.usage.output]].forEach(([label, value]) => {
                        const row = element('div', undefined, 'agent-token-row');
                        row.append(element('span', label));
                        const track = element('div', undefined, 'agent-token-track');
                        const bar = element('div', undefined, 'agent-token-bar ' + label.toLowerCase());
                        bar.style.width = (100 * value / maxTokens) + '%';
                        track.append(bar);
                        row.append(track, element('strong', value.toLocaleString()));
                        card.append(row);
                    });
                    card.append(element('p', 'Cache read ' + item.usage.cache_read.toLocaleString() + ' · Cache write ' + item.usage.cache_write.toLocaleString() + ' · Total tokens ' + item.usage.total_tokens.toLocaleString(), 'agent-call-meta'));
                }
            }
            if (item.kind === 'operation') {
                const details = element('details', undefined, 'agent-operation');
                const summary = element('summary', item.label);
                summary.style.paddingLeft = (1 + item.depth) + 'rem';
                details.append(summary, card);
                return details;
            }
            return card;
        }
        if (!agent.turns.length) container.append(element('p', 'No turn boundaries recorded.', 'section-note'));
        (agent.turns || []).forEach(turn => {
            const group = element('section', undefined, 'agent-turn');
            const title = element('button', turn.label + ' · ' + turn.status, 'agent-turn-title');
            title.type = 'button'; title.dataset.timelineStage = turn.stage_id;
            group.append(title);
            turn.items.forEach(item => group.append(renderItem(item)));
            if (!turn.items.length) group.append(element('p', 'No calls or operations recorded for this turn.', 'section-note'));
            container.append(group);
        });
        if (agent.operations && agent.operations.length) {
            container.append(element('h4', 'Outside recorded turns'));
            agent.operations.forEach(item => container.append(renderItem(item)));
        }
        if (agent.warnings && agent.warnings.length) {
            const details = element('details', undefined, 'recording-warning');
            details.append(element('summary', agent.warnings.length + ' recording diagnostics'));
            agent.warnings.forEach(warning => details.append(element('p', warning)));
            container.append(details);
        }
    }
    window.CCRStageViews = {renderCall, renderAgent};
}());
