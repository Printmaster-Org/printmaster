// Model the reviewed three-stage contract instead of returning legacy full rows
// from every endpoint. Shared by Server UI regression fixtures.
const indexFields = ['serial', 'agent_id', 'ip', 'manufacturer', 'model', 'hostname', 'location', 'asset_number', 'last_seen', 'status_messages', 'device_type', 'source_type', 'is_usb', 'spooler_status', 'page_count'];
function inventoryResponse(route, devices) {
    const pathname = new URL(route.request().url()).pathname;
    if (!['/api/v1/devices/index', '/api/v1/devices/rows', '/api/v1/devices/metrics/query'].includes(pathname)) return null;
    let body;
    if (pathname.endsWith('/index')) {
        body = devices.map(device => Object.fromEntries(indexFields.filter(field => device[field] !== undefined).map(field => [field, device[field]])));
    } else {
        const keys = route.request().postDataJSON().serials;
        if (keys.length > 100) return route.fulfill({ status: 400, body: 'too many keys' });
        body = devices.filter(device => keys.includes(device.serial));
        if (pathname.endsWith('/metrics/query')) body = body.map(device => ({
            id: 0, serial: device.serial, agent_id: device.agent_id,
            timestamp: new Date().toISOString(), page_count: device.page_count,
            color_pages: device.raw_data?.color_pages, mono_pages: device.raw_data?.mono_pages,
            scan_count: device.raw_data?.scan_count, toner_levels: device.toner_levels,
        }));
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
}
module.exports = { inventoryResponse };