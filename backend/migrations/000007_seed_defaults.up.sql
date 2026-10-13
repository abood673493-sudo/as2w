-- 000007_seed_defaults
-- Baseline application settings. Values are intentionally conservative and can
-- be edited from the panel (they are re-validated server side on every write).

INSERT INTO app_settings (key, value, description) VALUES
    ('ssh.port',            '22'::jsonb,              'SSH daemon TCP port advertised to clients'),
    ('ssh.managed',         'false'::jsonb,           'Whether the panel manages sshd configuration'),
    ('ws.path',             '"/ssh"'::jsonb,          'WebSocket upgrade path of the SSH gateway'),
    ('ws.enabled',          'true'::jsonb,            'Enable the SSH-over-WebSocket gateway'),
    ('ws.requires_tls',     'true'::jsonb,            'Advertise WSS as the recommended transport'),
    ('panel.public_hostname', 'null'::jsonb,          'Primary hostname used by the dashboard'),
    ('accounting.mode',     '"gateway"'::jsonb,       'Primary byte accounting mechanism'),
    ('quota.enforcement',   '"disable"'::jsonb,       'Action applied when an account exceeds its quota'),
    ('session.track_tcp',   'false'::jsonb,           'Track plain SSH TCP sessions (requires procfs sampling)'),
    ('certificates.auto_renew', 'true'::jsonb,        'Automatically renew Let''s Encrypt certificates')
ON CONFLICT (key) DO NOTHING;