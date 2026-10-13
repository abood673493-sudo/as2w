DELETE FROM app_settings WHERE key IN (
    'ssh.port', 'ssh.managed', 'ws.path', 'ws.enabled', 'ws.requires_tls',
    'panel.public_hostname', 'accounting.mode', 'quota.enforcement',
    'session.track_tcp', 'certificates.auto_renew'
);