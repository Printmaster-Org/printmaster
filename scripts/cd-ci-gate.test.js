const { verifyCI } = require('./cd-ci-gate');

const sha = 'a'.repeat(40);
const repository = 'Printmaster-Org/printmaster';
function fixture(eventName = 'workflow_run') {
    const run = {
        id: 123, workflow_id: 7, head_sha: sha, run_attempt: 2,
        head_branch: 'main', head_repository: { full_name: repository },
        status: 'completed', conclusion: 'success', event: 'push',
    };
    const aggregate = { name: 'CI Passed', status: 'completed', conclusion: 'success' };
    const input = { eventName, sha, repository, event: { workflow_run: { ...run } } };
    const calls = [];
    const api = jest.fn(async (endpoint, collection) => {
        calls.push([endpoint, collection]);
        if (endpoint.endsWith('/actions/workflows/ci.yml')) return { id: 7 };
        if (endpoint.includes('/runs?head_sha=')) return [run];
        if (endpoint.endsWith('/actions/runs/123')) return run;
        if (endpoint.endsWith('/attempts/2/jobs?per_page=100')) return [aggregate];
        if (endpoint.endsWith('/git/ref/heads/main')) return { object: { sha } };
        throw new Error(`Unexpected API request ${endpoint}`);
    });
    return { input, api, run, aggregate, calls };
}

test.each(['workflow_run', 'push', 'workflow_dispatch'])('%s verifies completed exact-SHA CI without polling', async eventName => {
    const f = fixture(eventName);
    await expect(verifyCI(f.input, f.api)).resolves.toEqual({ publish: true, sha, runId: 123 });
    expect(f.calls.filter(([path]) => path.includes('/jobs?'))).toEqual([
        [`repos/${repository}/actions/runs/123/attempts/2/jobs?per_page=100`, 'jobs'],
    ]);
    expect(f.calls).toHaveLength(eventName === 'workflow_run' ? 4 : 3);
});

test.each([
    ['wrong SHA', run => { run.head_sha = 'b'.repeat(40); }],
    ['different workflow', run => { run.workflow_id = 8; }],
    ['foreign repository', run => { run.head_repository.full_name = 'foreign/repository'; }],
    ['missing repository', run => { delete run.head_repository; }],
    ['pull request', run => { run.event = 'pull_request'; }],
    ['branch', run => { run.head_branch = 'feature'; }],
    ['older attempt', run => { run.run_attempt = 3; }],
    ['running', run => { run.status = 'in_progress'; }],
    ['failure', run => { run.conclusion = 'failure'; }],
    ['cancelled', run => { run.conclusion = 'cancelled'; }],
    ['skipped', run => { run.conclusion = 'skipped'; }],
])('rejects %s CI completion', async (_, change) => {
    const f = fixture();
    change(f.run);
    await expect(verifyCI(f.input, f.api)).rejects.toThrow();
});

test.each(['failure', 'skipped', 'cancelled', null])('aggregate %s cannot authorize CD', async conclusion => {
    const f = fixture();
    f.aggregate.conclusion = conclusion;
    await expect(verifyCI(f.input, f.api)).rejects.toThrow('CI Passed');
});

test('superseded main completion skips development publication', async () => {
    const f = fixture();
    const original = f.api.getMockImplementation();
    f.api.mockImplementation((path, collection) => path.endsWith('/git/ref/heads/main') ?
        { object: { sha: 'b'.repeat(40) } } : original(path, collection));
    await expect(verifyCI(f.input, f.api)).resolves.toMatchObject({ publish: false, sha });
});

test.each(['push', 'workflow_dispatch'])('%s rejects absent CI immediately', async eventName => {
    const f = fixture(eventName);
    const original = f.api.getMockImplementation();
    f.api.mockImplementation((path, collection) => collection === 'workflow_runs' ? [] : original(path, collection));
    await expect(verifyCI(f.input, f.api)).rejects.toThrow('run CI first');
    expect(f.api).toHaveBeenCalledTimes(2);
});

test('manual/tag gate rejects a newer failed trusted run instead of reusing old success', async () => {
    const f = fixture('push');
    const original = f.api.getMockImplementation();
    f.api.mockImplementation((path, collection) => collection === 'workflow_runs' ?
        [f.run, { ...f.run, id: 124, conclusion: 'failure' }] : original(path, collection));
    await expect(verifyCI(f.input, f.api)).rejects.toThrow('run CI first');
});

test.each([
    { jobs: [] },
    { jobs: [{ name: 'different', status: 'completed', conclusion: 'success' }] },
])('missing aggregate rejects CD', async ({ jobs }) => {
    const f = fixture();
    const original = f.api.getMockImplementation();
    f.api.mockImplementation((path, collection) => collection === 'jobs' ? jobs : original(path, collection));
    await expect(verifyCI(f.input, f.api)).rejects.toThrow('CI Passed');
});

test('API failure is explicit and does not retry or publish', async () => {
    const f = fixture();
    f.api.mockRejectedValue(new Error('API unavailable'));
    await expect(verifyCI(f.input, f.api)).rejects.toThrow('API unavailable');
    expect(f.api).toHaveBeenCalledTimes(1);
});
