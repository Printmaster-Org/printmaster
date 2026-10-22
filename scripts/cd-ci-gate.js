const fs = require('fs');
const { execFileSync } = require('child_process');

// No polling: automatic CD is scheduled by CI completion. Explicit tag/manual
// publication must already have a successful trusted CI run for the exact SHA.
async function verifyCI({ eventName, event, sha, repository }, api) {
    if (!/^[a-f0-9]{40}$/.test(sha) || !/^[\w.-]+\/[\w.-]+$/.test(repository)) {
        throw new Error('Invalid CD source SHA or repository');
    }
    const base = `repos/${repository}`;
    const workflow = await api(`${base}/actions/workflows/ci.yml`);
    const trusted = run => run.head_sha === sha && run.workflow_id === workflow.id &&
        run.head_repository?.full_name === repository &&
        (run.event === 'push' && run.head_branch === 'main' || run.event === 'workflow_dispatch');
    let run;
    if (eventName === 'workflow_run') {
        run = await api(`${base}/actions/runs/${event.workflow_run.id}`);
        if (!trusted(run) || run.head_branch !== 'main' ||
            event.workflow_run.head_sha !== sha || run.run_attempt !== event.workflow_run.run_attempt) {
            throw new Error('CI completion does not identify the trusted main workflow attempt');
        }
    } else if (eventName === 'push' || eventName === 'workflow_dispatch') {
        const runs = await api(`${base}/actions/workflows/ci.yml/runs?head_sha=${sha}&per_page=100`, 'workflow_runs');
        run = runs.filter(trusted).sort((left, right) => right.id - left.id)[0];
    } else {
        throw new Error('Unsupported CD event');
    }
    if (!run || run.status !== 'completed' || run.conclusion !== 'success') {
        throw new Error('Successful completed CI is required for this exact SHA; run CI first, then retry CD');
    }
    const jobs = await api(`${base}/actions/runs/${run.id}/attempts/${run.run_attempt}/jobs?per_page=100`, 'jobs');
    const aggregate = jobs.filter(job => job.name === 'CI Passed');
    if (aggregate.length !== 1 || aggregate[0].status !== 'completed' || aggregate[0].conclusion !== 'success') {
        throw new Error('The exact CI attempt must contain a successful CI Passed aggregate');
    }
    if (eventName === 'workflow_run') {
        const main = await api(`${base}/git/ref/heads/main`);
        if (main.object.sha !== sha) {
            return { publish: false, sha, runId: run.id, reason: 'Superseded main commit; no development publication' };
        }
    }
    return { publish: true, sha, runId: run.id };
}

async function main() {
    const event = JSON.parse(fs.readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8'));
    const api = async (endpoint, collection) => {
        const args = ['api', '--method', 'GET', endpoint];
        if (collection) args.push('--paginate', '--slurp');
        const data = JSON.parse(execFileSync('gh', args, { encoding: 'utf8', timeout: 60000, maxBuffer: 16 * 1024 * 1024 }));
        return collection ? data.flatMap(page => page[collection]) : data;
    };
    const result = await verifyCI({
        eventName: process.env.GITHUB_EVENT_NAME, event,
        sha: process.env.SOURCE_SHA, repository: process.env.GITHUB_REPOSITORY,
    }, api);
    fs.appendFileSync(process.env.GITHUB_OUTPUT, `publish=${result.publish}\n`);
    console.log(JSON.stringify(result));
}

if (require.main === module) {
    main().catch(error => {
        console.error(`CD CI gate failed: ${error.message}`);
        process.exitCode = 1;
    });
}

module.exports = { verifyCI };
