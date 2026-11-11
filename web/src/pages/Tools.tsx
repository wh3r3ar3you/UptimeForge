import {
  Alert, Badge, Box, Button, Card, Code, Group, MultiSelect, Progress, SegmentedControl, SimpleGrid, Stack, Text, TextInput, Textarea,
  Tooltip,
} from '@mantine/core';
import {
  IconAlertCircle, IconBolt, IconClockHour4, IconFlask2, IconGauge, IconPlayerPlay, IconRoute, IconWorld,
} from '@tabler/icons-react';
import { useMutation } from '@tanstack/react-query';
import { useEffect, useMemo, useRef, useState } from 'react';
import { api, ApiError, useProbes, type ProbeStatus, type RunOutcome, type RunRow } from '../api';
import { Section } from '../components/Cards';
import { PageHeader } from '../components/Misc';
import { classify, useReason, useWhy } from '../components/ProbeLines';
import { Dot, KindIcon, kindColor, Legend, VerdictBadge } from '../components/Status';
import { flag, ms } from '../format';
import { plural, useLang, useT } from '../i18n';
import { status } from '../theme';

const L = {
  ru: {
    title: 'Проверка конфигов', subtitle: 'Проверьте ссылку, прокси или подписку со всех проб сразу — до того, как добавлять в мониторинг',
    mode_single: 'Один конфиг', mode_sub: 'Подписка целиком', target: 'Что проверить', sub_url: 'URL подписки',
    target_hint: 'Ссылка vless:// vmess:// trojan:// ss:// hy2://, MTProxy tg://proxy или t.me/proxy, http(s)-адрес или host:port',
    sub_hint: 'Хаб скачает подписку и проверит каждый конфиг (до 100) с выбранных проб',
    ua: 'User-Agent', ua_hint: 'Необязательно. Панели (Remnawave, Marzban, 3x-ui) выбирают формат ответа по User-Agent',
    probes: 'С каких проб', probes_hint: 'По умолчанию — все онлайн', all_online: 'Все онлайн', offline: 'офлайн', no_net: 'без интернета',
    run: 'Проверить', running: 'Проверяем… {0} с', running_hint: 'Ждём ответы всех проб — обычно до 30 секунд',
    running_sub: 'Подписка проверяется по 8 конфигов параллельно — это может занять пару минут',
    result: 'Результат', latency: 'задержка', failed_after: 'ошибка через', speed: 'скорость', mbps: 'Мбит/с', exit: 'Выход в интернет', phases: 'Фазы',
    v_ok: 'Работает со всех проб · {0} из {0}', v_down: 'Не работает ни с одной пробы', v_blocked: 'Блок в {0}',
    v_partial: 'Частично · не работает с {0} из {1}', v_unknown: 'Ни одна проба не ответила',
    excluded: 'Не учтены в вердикте: {0} — офлайн или без интернета', not_counted: 'не учтена: у пробы нет интернета',
    configs: '{0} {1}', works: 'работают', fails: 'не работают', blocked: 'блок', partial: 'частично',
    legend_up: 'работает', legend_down: 'не работает', legend_none: 'нет ответа', config: 'Конфиг',
    phase_connect: 'TCP', phase_tls: 'TLS', phase_ttfb: 'Первый байт', phase_rtt: 'RTT', phase_download: 'Загрузка',
    empty: 'Вставьте конфиг и нажмите «Проверить»', empty_hint: 'Каждая проба проверит его из своей сети: так сразу видно, где он заблокирован',
  },
  en: {
    title: 'Config checker', subtitle: 'Test a link, proxy or subscription from every probe at once — before adding it to monitoring',
    mode_single: 'Single target', mode_sub: 'Whole subscription', target: 'What to check', sub_url: 'Subscription URL',
    target_hint: 'A vless:// vmess:// trojan:// ss:// hy2:// link, a tg://proxy or t.me/proxy MTProxy link, an http(s) URL or host:port',
    sub_hint: 'The hub downloads the subscription and checks every config (up to 100) from the selected probes',
    ua: 'User-Agent', ua_hint: 'Optional. Panels (Remnawave, Marzban, 3x-ui) pick the response format by User-Agent',
    probes: 'From probes', probes_hint: 'Defaults to every online probe', all_online: 'All online', offline: 'offline', no_net: 'no internet',
    run: 'Run', running: 'Checking… {0}s', running_hint: 'Waiting for every probe to answer — usually under 30 seconds',
    running_sub: 'Configs are checked 8 at a time — this can take a couple of minutes',
    result: 'Result', latency: 'latency', failed_after: 'failed after', speed: 'throughput', mbps: 'Mbit/s', exit: 'Exits to the internet', phases: 'Phases',
    v_ok: 'Works from every probe · {0} of {0}', v_down: 'Fails from every probe', v_blocked: 'Blocked in {0}',
    v_partial: 'Partial · fails from {0} of {1}', v_unknown: 'No probe answered',
    excluded: 'Left out of the verdict: {0} — offline or without internet', not_counted: 'not counted: the probe has no internet',
    configs: '{0} {1}', works: 'up', fails: 'down', blocked: 'blocked', partial: 'partial',
    legend_up: 'up', legend_down: 'down', legend_none: 'no answer', config: 'Config',
    phase_connect: 'TCP', phase_tls: 'TLS', phase_ttfb: 'First byte', phase_rtt: 'RTT', phase_download: 'Download',
    empty: 'Paste a config and press Run', empty_hint: 'Every probe tests it from its own network, so you see at once where it is blocked',
  },
};

type Mode = 'auto' | 'subscription';
const PHASES = ['connect', 'tls', 'ttfb', 'rtt', 'download'] as const;

/** Lines that count for the verdict: probes that answered and have internet, as in the hub's consensus. */
function verdictLines(outcomes: RunOutcome[], noNet: Set<string>) {
  return outcomes
    .filter((o) => o.result && !noNet.has(o.probe))
    .map((o) => ({ ok: o.result!.status === 'up', region: o.region, reason: o.result!.reason, bytes: o.result!.bytes }));
}

function mbps(bps?: number) {
  return bps ? (bps * 8) / 1e6 : null;
}

function useVerdictText() {
  const t = useT(L);
  return (kind: string, region: string | undefined, lines: { ok: boolean }[]) => {
    const failing = lines.filter((l) => !l.ok).length;
    switch (kind) {
      case 'ok': return t('v_ok', lines.length);
      case 'down': return t('v_down');
      case 'blocked': return t('v_blocked', (region ?? '').toUpperCase());
      case 'partial': return t('v_partial', failing, lines.length);
    }
    return t('v_unknown');
  };
}

function Running({ mode }: { mode: Mode }) {
  const t = useT(L);
  const [s, setS] = useState(0);
  useEffect(() => {
    const start = Date.now();
    const id = setInterval(() => setS(Math.floor((Date.now() - start) / 1000)), 250);
    return () => clearInterval(id);
  }, []);
  const expected = mode === 'subscription' ? 90 : 30;
  const value = 100 * (1 - Math.exp((-1.6 * s) / expected));
  return (
    <Card padding="lg">
      <Stack gap="sm">
        <Group justify="space-between">
          <Text fw={600}>{t('running', s)}</Text>
          <Text size="sm" c="dimmed" className="uf-num">{Math.round(value)}%</Text>
        </Group>
        <Progress value={value} animated striped size="md" radius="xl" />
        <Text size="sm" c="dimmed">{mode === 'subscription' ? t('running_sub') : t('running_hint')}</Text>
      </Stack>
    </Card>
  );
}

function Metric({ icon: I, label, value, unit }: { icon: typeof IconBolt; label: string; value: string; unit?: string }) {
  return (
    <Stack gap={0}>
      <Group gap={4} wrap="nowrap">
        <I size={13} style={{ opacity: 0.55 }} />
        <Text size="xs" c="dimmed" tt="uppercase" fw={600} style={{ letterSpacing: '0.04em' }}>{label}</Text>
      </Group>
      <Text ff="monospace" fw={700} fz={22} lh={1.2}>
        {value}{unit && <Text span size="sm" c="dimmed" fw={500} ml={4}>{unit}</Text>}
      </Text>
    </Stack>
  );
}

function OutcomeCard({ o, noNet }: { o: RunOutcome; noNet: boolean }) {
  const t = useT(L);
  const reason = useReason();
  const r = o.result;
  const up = r?.status === 'up';
  const speed = mbps(r?.bps);
  const phases = PHASES.filter((p) => (r?.phases?.[p] ?? 0) > 0);
  const info = Object.entries(r?.info ?? {}).filter(([k]) => k !== 'egress_ip' && k !== 'egress_loc');
  const kind = !r ? (o.error === 'offline' ? 'probe_offline' : 'unknown') : up ? 'ok' : 'down';
  return (
    <Card padding="md" className="uf-card" style={{ opacity: r ? 1 : 0.75 }}>
      <Stack gap="sm">
        <Group justify="space-between" wrap="nowrap" gap="xs">
          <Text fw={650} truncate>
            <Box component="span" mr={6}>{flag(o.region) || '📍'}</Box>{o.name}
          </Text>
          <VerdictBadge kind={kind} size="sm" />
        </Group>
        {r && (
          <Group gap="xl" align="flex-end">
            <Metric icon={IconClockHour4} label={up ? t('latency') : t('failed_after')} value={ms(r.latency_ms)} />
            {speed != null && <Metric icon={IconGauge} label={t('speed')} value={speed >= 100 ? speed.toFixed(0) : speed.toFixed(1)} unit={t('mbps')} />}
          </Group>
        )}
        {phases.length > 0 && (
          <Group gap={6}>
            {phases.map((p) => (
              <Badge key={p} variant="default" size="md" radius="sm" styles={{ root: { textTransform: 'none', fontWeight: 500 } }}>
                <Text span size="xs" c="dimmed">{t(`phase_${p}`)}</Text>{' '}
                <Text span size="xs" ff="monospace" fw={600}>{ms(r!.phases![p])}</Text>
              </Badge>
            ))}
          </Group>
        )}
        {r?.info?.egress_ip && (
          <Group gap={8} wrap="nowrap" align="flex-start">
            <IconWorld size={16} style={{ flex: 'none', marginTop: 2, opacity: 0.6 }} />
            <Box style={{ minWidth: 0 }}>
              <Text size="xs" c="dimmed">{t('exit')}</Text>
              <Text size="sm" style={{ wordBreak: 'break-word' }}>
                <Text span ff="monospace" fw={600}>{r.info.egress_ip}</Text>
                {r.info.egress_loc && <Text span c="dimmed"> · {r.info.egress_loc}</Text>}
              </Text>
            </Box>
          </Group>
        )}
        {info.length > 0 && (
          <Group gap={6}>
            {info.map(([k, v]) => (
              <Badge key={k} variant="outline" color="gray" size="sm" styles={{ root: { textTransform: 'none', fontWeight: 500 } }}>{k}: {v}</Badge>
            ))}
          </Group>
        )}
        {(r?.status === 'down' || o.error) && (
          <Box
            p="xs"
            style={{
              borderRadius: 8, background: `color-mix(in srgb, ${status.down} 8%, transparent)`,
              border: `1px solid color-mix(in srgb, ${status.down} 25%, transparent)`,
            }}
          >
            <Text size="sm" fw={600}>{r ? reason(r.reason, r.error, r.bytes) : reason(o.error, o.error)}</Text>
            {r?.error && r.reason && <Text size="xs" c="dimmed" ff="monospace" style={{ wordBreak: 'break-all' }}>{r.error}</Text>}
          </Box>
        )}
        {noNet && r && <Text size="xs" c="dimmed">{t('not_counted')}</Text>}
      </Stack>
    </Card>
  );
}

function SingleResult({ row, noNet }: { row: RunRow; noNet: Set<string> }) {
  const t = useT(L);
  const why = useWhy();
  const verdictText = useVerdictText();
  const lines = verdictLines(row.outcomes ?? [], noNet);
  const v = classify(lines);
  const excluded = (row.outcomes?.length ?? 0) - lines.length;
  const explain = v.kind === 'ok' || v.kind === 'unknown' ? '' : why(v.kind, lines, { region: v.region });
  return (
    <Stack gap="md">
      <Card padding="lg" style={{ borderLeft: `3px solid ${kindColor(v.kind)}` }}>
        <Stack gap={8}>
          <Group gap="sm" wrap="wrap">
            <VerdictBadge kind={v.kind} region={v.region} size="lg" />
            <Text fw={650} fz="lg">{verdictText(v.kind, v.region, lines)}</Text>
          </Group>
          <Group gap={8} wrap="wrap">
            <Badge variant="default" size="sm" styles={{ root: { textTransform: 'uppercase' } }}>{row.type}</Badge>
            <Text size="sm" fw={500} style={{ wordBreak: 'break-all' }}>{row.name}</Text>
            {row.target && row.target !== row.name && <Code fz="xs" style={{ wordBreak: 'break-all' }}>{row.target}</Code>}
          </Group>
          {explain && <Text size="sm" c="dimmed">{explain}</Text>}
          {excluded > 0 && <Text size="xs" c="dimmed">{t('excluded', excluded)}</Text>}
          {row.error && <Alert color="red" variant="light" icon={<IconAlertCircle size={16} />}>{row.error}</Alert>}
        </Stack>
      </Card>
      <SimpleGrid cols={{ base: 1, sm: 2, xl: 3 }} spacing="md">
        {(row.outcomes ?? []).map((o) => <OutcomeCard key={o.probe} o={o} noNet={noNet.has(o.probe)} />)}
      </SimpleGrid>
    </Stack>
  );
}

function cellStyle(o?: RunOutcome) {
  if (!o?.result) return { background: 'var(--uf-track)', color: 'var(--mantine-color-dimmed)' };
  if (o.result.status === 'down') return { background: status.down, color: '#fff' };
  return { background: status.ok, color: '#fff' };
}

function cellText(o?: RunOutcome) {
  if (!o?.result) return '–';
  if (o.result.status === 'down') return '✕';
  const l = o.result.latency_ms;
  return l >= 1000 ? `${(l / 1000).toFixed(1)}s` : `${Math.round(l)}`;
}

function SubscriptionResult({ rows, noNet }: { rows: RunRow[]; noNet: Set<string> }) {
  const t = useT(L);
  const reason = useReason();
  const { lang } = useLang();
  const probes = useMemo(() => {
    const m = new Map<string, { id: string; name: string; region?: string }>();
    for (const r of rows) for (const o of r.outcomes ?? []) if (!m.has(o.probe)) m.set(o.probe, { id: o.probe, name: o.name, region: o.region });
    return [...m.values()];
  }, [rows]);
  const verdicts = rows.map((r) => classify(verdictLines(r.outcomes ?? [], noNet)));
  const count = (k: string) => verdicts.filter((v) => v.kind === k).length;
  const cols = `minmax(150px, 320px) repeat(${probes.length}, 56px)`;
  const configsWord = lang === 'ru' ? plural(rows.length, 'конфиг', 'конфига', 'конфигов') : rows.length === 1 ? 'config' : 'configs';

  return (
    <Card padding="lg">
      <Stack gap="md">
        <Group gap="lg" wrap="wrap">
          <Text fw={650} fz="lg">{t('configs', rows.length, configsWord)}</Text>
          {([['ok', 'works'], ['partial', 'partial'], ['blocked', 'blocked'], ['down', 'fails']] as const).map(([k, label]) =>
            count(k) > 0 && (
              <Group key={k} gap={6} wrap="nowrap">
                <KindIcon kind={k} size={16} />
                <Text size="sm"><Text span fw={700} className="uf-num">{count(k)}</Text> {t(label)}</Text>
              </Group>
            ))}
        </Group>
        <Box className="uf-scroll-x">
          <Box className="uf-matrix" style={{ gridTemplateColumns: cols, minWidth: 160 + probes.length * 60, rowGap: 8 }}>
            <Text size="xs" c="dimmed" fw={700} tt="uppercase" style={{ letterSpacing: '0.06em' }}>{t('config')}</Text>
            {probes.map((p) => (
              <Tooltip key={p.id} label={p.name}>
                <Stack gap={0} align="center" style={{ minWidth: 0 }}>
                  <Text size="sm" lh={1.2}>{flag(p.region) || '📍'}</Text>
                  <Text size="xs" c="dimmed" ta="center" truncate fw={600} px={2} maw="100%">{p.name}</Text>
                </Stack>
              </Tooltip>
            ))}
            {rows.map((r, i) => {
              const cells = new Map((r.outcomes ?? []).map((o) => [o.probe, o]));
              const v = verdicts[i];
              return [
                <Tooltip key={`n-${i}`} label={<Stack gap={2}><Text size="xs" fw={600}>{r.name}</Text><Text size="xs">{r.target}</Text>{r.error && <Text size="xs" c="red.3">{r.error}</Text>}</Stack>} multiline maw={360}>
                  <Group gap={6} wrap="nowrap" pr="xs" style={{ minWidth: 0 }}>
                    <KindIcon kind={r.error ? 'down' : v.kind} size={15} />
                    <Box style={{ minWidth: 0 }}>
                      <Text size="sm" fw={500} truncate>{r.name || r.target}</Text>
                      <Text size="xs" c="dimmed" truncate ff="monospace">{r.target}</Text>
                    </Box>
                  </Group>
                </Tooltip>,
                ...probes.map((p) => {
                  const o = cells.get(p.id);
                  const res = o?.result;
                  return (
                    <Tooltip
                      key={`${i}-${p.id}`}
                      multiline
                      maw={340}
                      label={
                        <Stack gap={2}>
                          <Text size="xs" fw={700}>{r.name} · {p.name}</Text>
                          {!res ? <Text size="xs">{o?.error ? reason(o.error, o.error) : t('legend_none')}</Text> : res.status === 'up' ? (
                            <Text size="xs">{t('legend_up')} · {ms(res.latency_ms)}{res.bps ? ` · ${mbps(res.bps)!.toFixed(1)} ${t('mbps')}` : ''}</Text>
                          ) : (
                            <>
                              <Text size="xs">{reason(res.reason, res.error, res.bytes)}</Text>
                              {res.error && <Text size="xs" c="red.3" style={{ wordBreak: 'break-all' }}>{res.error}</Text>}
                            </>
                          )}
                          {res?.info?.egress_ip && <Text size="xs" c="dimmed">{res.info.egress_ip} {res.info.egress_loc}</Text>}
                        </Stack>
                      }
                    >
                      <Box className="uf-cell" style={cellStyle(o)}>{cellText(o)}</Box>
                    </Tooltip>
                  );
                }),
              ];
            })}
          </Box>
        </Box>
        <Legend items={[
          { color: status.ok, label: `${t('legend_up')} · ms` }, { color: status.down, label: t('legend_down') },
          { color: 'var(--uf-track)', label: t('legend_none') },
        ]} />
      </Stack>
    </Card>
  );
}

export default function Tools() {
  const t = useT(L);
  const probesQ = useProbes();
  const [mode, setMode] = useState<Mode>('auto');
  const [target, setTarget] = useState('');
  const [ua, setUa] = useState('');
  const [selected, setSelected] = useState<string[]>([]);
  const inited = useRef(false);
  const probes: ProbeStatus[] = probesQ.data?.probes ?? [];
  const online = probes.filter((p) => p.online).map((p) => p.id);
  const noNet = useMemo(() => new Set(probes.filter((p) => p.online && !p.internet).map((p) => p.id)), [probes]);

  useEffect(() => {
    if (!inited.current && probesQ.data) {
      inited.current = true;
      setSelected(probesQ.data.probes.filter((p) => p.online).map((p) => p.id));
    }
  }, [probesQ.data]);

  const run = useMutation<{ rows: RunRow[]; mode: Mode }, ApiError, void>({
    mutationFn: async () => {
      const res = await api<{ rows: RunRow[] }>('POST', '/api/v1/tools/run', {
        target: target.trim(), probes: selected, mode, user_agent: mode === 'subscription' ? ua.trim() : '',
      });
      return { rows: res.rows ?? [], mode };
    },
  });

  const canRun = target.trim() !== '' && selected.length > 0 && !run.isPending;
  const go = () => canRun && run.mutate();

  return (
    <>
      <PageHeader title={t('title')} subtitle={t('subtitle')} />
      <Stack gap="xl">
        <Card padding="lg">
          <Stack gap="md">
            <SegmentedControl
              value={mode}
              onChange={(v) => setMode(v as Mode)}
              data={[{ value: 'auto', label: t('mode_single') }, { value: 'subscription', label: t('mode_sub') }]}
              style={{ alignSelf: 'flex-start' }}
              maw="100%"
            />
            {mode === 'auto' ? (
              <Textarea
                label={t('target')}
                description={t('target_hint')}
                placeholder={'vless://…@host:443?security=reality&…#name\ntg://proxy?server=…&port=443&secret=…\nhost:443'}
                autosize
                minRows={3}
                maxRows={8}
                value={target}
                onChange={(e) => setTarget(e.currentTarget.value)}
                onKeyDown={(e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) go(); }}
                styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 13, wordBreak: 'break-all' } }}
                spellCheck={false}
              />
            ) : (
              <SimpleGrid cols={{ base: 1, md: 2 }} spacing="md">
                <TextInput
                  label={t('sub_url')}
                  description={t('sub_hint')}
                  placeholder="https://sub.example.com/abc123"
                  value={target}
                  onChange={(e) => setTarget(e.currentTarget.value)}
                  onKeyDown={(e) => { if (e.key === 'Enter') go(); }}
                  styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 13 } }}
                  spellCheck={false}
                />
                <TextInput
                  label={t('ua')}
                  description={t('ua_hint')}
                  placeholder="v2rayNG/1.9.30"
                  value={ua}
                  onChange={(e) => setUa(e.currentTarget.value)}
                  styles={{ input: { fontFamily: 'var(--mantine-font-family-monospace)', fontSize: 13 } }}
                  spellCheck={false}
                />
              </SimpleGrid>
            )}
            <MultiSelect
              label={t('probes')}
              description={t('probes_hint')}
              data={probes.map((p) => ({
                value: p.id,
                label: `${flag(p.region) || '📍'} ${p.name}`,
                disabled: !p.online,
              }))}
              renderOption={({ option }) => {
                const p = probes.find((x) => x.id === option.value);
                return (
                  <Group gap={8} wrap="nowrap">
                    <Dot color={!p?.online ? status.down : !p.internet ? status.partial : status.ok} />
                    <Text size="sm">{option.label}</Text>
                    {p && !p.online && <Text size="xs" c="dimmed">{t('offline')}</Text>}
                    {p?.online && !p.internet && <Text size="xs" c="dimmed">{t('no_net')}</Text>}
                  </Group>
                );
              }}
              value={selected}
              onChange={setSelected}
              searchable
              clearable
              hidePickedOptions={false}
            />
            <Group justify="space-between" wrap="wrap" gap="sm">
              <Button variant="subtle" size="xs" onClick={() => setSelected(online)} disabled={!probes.length}>{t('all_online')}</Button>
              <Button
                leftSection={<IconPlayerPlay size={16} />}
                onClick={go}
                disabled={!canRun}
                loading={run.isPending}
                variant="gradient"
                gradient={{ from: '#20cfae', to: '#2a8cf0', deg: 90 }}
                miw={160}
              >
                {t('run')}
              </Button>
            </Group>
          </Stack>
        </Card>

        {run.isPending && <Running mode={mode} />}
        {run.isError && !run.isPending && (
          <Alert color="red" variant="light" icon={<IconAlertCircle size={18} />} title={t('result')}>{run.error.message}</Alert>
        )}
        {run.data && !run.isPending && (
          <Section title={t('result')}>
            {run.data.mode === 'subscription'
              ? <SubscriptionResult rows={run.data.rows} noNet={noNet} />
              : run.data.rows[0] && <SingleResult row={run.data.rows[0]} noNet={noNet} />}
          </Section>
        )}
        {!run.data && !run.isPending && !run.isError && (
          <Card padding="xl" style={{ borderStyle: 'dashed', background: 'transparent' }}>
            <Stack align="center" gap={6} py="md">
              <Group gap="xs" c="dimmed"><IconFlask2 size={22} /><IconRoute size={22} /></Group>
              <Text fw={600}>{t('empty')}</Text>
              <Text size="sm" c="dimmed" ta="center" maw={480}>{t('empty_hint')}</Text>
            </Stack>
          </Card>
        )}
      </Stack>
    </>
  );
}
