'use strict';

const fs = require('node:fs');
const { spawnSync } = require('node:child_process');

function getReminderDate(expiry, monthsBefore = 6) {
    const expiration = new Date(expiry);
    if (!Number.isFinite(expiration.getTime()) || !Number.isInteger(monthsBefore) || monthsBefore < 1) {
        throw new Error('invalid root expiry or reminder interval');
    }

    const target = new Date(Date.UTC(
        expiration.getUTCFullYear(),
        expiration.getUTCMonth() - monthsBefore,
        1,
        expiration.getUTCHours(),
        expiration.getUTCMinutes(),
        expiration.getUTCSeconds(),
        expiration.getUTCMilliseconds(),
    ));
    const lastDay = new Date(Date.UTC(target.getUTCFullYear(), target.getUTCMonth() + 1, 0)).getUTCDate();
    target.setUTCDate(Math.min(expiration.getUTCDate(), lastDay));
    return target;
}

function isReminderDue(expiry, now = new Date()) {
    const due = getReminderDate(expiry);
    const today = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
    const dueDay = Date.UTC(due.getUTCFullYear(), due.getUTCMonth(), due.getUTCDate());
    return today >= dueDay;
}

function main(rootPath) {
    const root = JSON.parse(fs.readFileSync(rootPath, 'utf8'));
    const version = root?.signed?.version;
    const expiry = root?.signed?.expires;
    if (!Number.isSafeInteger(version) || version < 1 || typeof expiry !== 'string') {
        throw new Error('embedded public root has no valid version/expiry');
    }

    const reminderDate = getReminderDate(expiry);
    const dueDate = reminderDate.toISOString().slice(0, 10);
    const title = `Review TUF root v${version} before ${expiry.slice(0, 10)} expiry`;
    if (!isReminderDue(expiry)) {
        console.log(`Root v${version} maintenance reminder is scheduled for ${dueDate} UTC.`);
        return;
    }

    const repository = process.env.GITHUB_REPOSITORY;
    if (!repository) throw new Error('GITHUB_REPOSITORY is required');

    const existing = spawnSync('gh', [
        'issue', 'list', '--repo', repository, '--state', 'all', '--limit', '100', '--json', 'title',
    ], { encoding: 'utf8' });
    if (existing.error || existing.status !== 0) throw new Error('could not check for an existing reminder issue');
    if (JSON.parse(existing.stdout).some(issue => issue.title === title)) {
        console.log(`Root v${version} maintenance issue already exists.`);
        return;
    }

    const body = [
        `The embedded TUF root v${version} expires on ${expiry}.`,
        '',
        `Review the owner-controlled root rotation by ${dueDate} UTC (six calendar months before expiry).`,
        '',
        '- Confirm offline root-key custody and independently stored backups.',
        '- Review the signing operations runbook and plan a dual-authorized root transition.',
        '- Verify the new root fingerprint independently before embedding or publishing it.',
        '- Refresh encrypted owner backups after any key/custody change.',
        '',
        'This is a reminder only. It does not rotate keys, sign metadata, publish artifacts, or enable publisher schedules.',
        'Never add private keys, credentials, recovery codes, or secret storage paths to this issue.',
        '',
        'Runbook: https://docs.printmaster.work/development/update-signing-operations/',
    ].join('\n');

    const created = spawnSync('gh', [
        'issue', 'create', '--repo', repository, '--title', title, '--assignee', 'mstrhakr', '--body', body,
    ], { encoding: 'utf8' });
    if (created.error || created.status !== 0) throw new Error('could not create the root-maintenance reminder issue');
    console.log('Opened the owner-assigned root-maintenance reminder issue.');
}

if (require.main === module) {
    try {
        main(process.argv[2] || 'agent/update_publisher_root.json');
    } catch (error) {
        console.error(`Root reminder failed: ${error.message}`);
        process.exitCode = 1;
    }
}

module.exports = { getReminderDate, isReminderDue };
