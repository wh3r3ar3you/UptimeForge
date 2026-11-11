import { Box, Group, Modal, SimpleGrid, Stack, Text, UnstyledButton } from '@mantine/core';
import { IconCheck } from '@tabler/icons-react';
import { useEffect } from 'react';
import { DEFAULT_NUM_FONT, DEFAULT_UI_FONT, loadAllFonts, NUM_FONTS, UI_FONTS, type UfFont } from '../fonts';
import { useT } from '../i18n';
import { usePrefs } from '../prefs';
import { mix, THEMES, type UfTheme } from '../themes';

const L = {
  ru: {
    title: 'Внешний вид', themes: 'Тема', classic: 'Классика', warm: 'Тёплые', bright: 'Яркие', light: 'Светлые',
    font: 'Шрифт интерфейса', num: 'Шрифт цифр', same: 'Как интерфейс',
    sample: 'Нода в сети', hint: 'Тема и шрифты сохраняются в вашем профиле и одинаковы на всех устройствах.',
  },
  en: {
    title: 'Appearance', themes: 'Theme', classic: 'Classic', warm: 'Warm', bright: 'Bright', light: 'Light',
    font: 'Interface font', num: 'Numbers font', same: 'Same as interface',
    sample: 'Node online', hint: 'The theme and fonts are saved to your profile and follow you to every device.',
  },
};

/** Preview draws a tiny app in the theme's colours: a nav column, an accent bar and surfaces. */
function Preview({ t }: { t: UfTheme }) {
  const nav = mix(t.surface, t.border, 0.6);
  return (
    <Box style={{ display: 'grid', gridTemplateColumns: '28% 1fr', gap: 6, padding: 8, height: 112, borderRadius: 10, background: t.body, border: `1px solid ${t.border}` }}>
      <Stack gap={5}>
        {[0, 1, 2, 3].map((i) => <Box key={i} h={18} style={{ borderRadius: 4, background: nav }} />)}
      </Stack>
      <Stack gap={5}>
        <Box h={20} style={{ borderRadius: 4, background: t.surface }} />
        <Box h={20} style={{ borderRadius: 4, background: t.primary }} />
        <Box h={20} style={{ borderRadius: 4, background: t.surface }} />
        <Box h={20} style={{ borderRadius: 4, background: mix(t.primary, t.surface, 0.7) }} />
      </Stack>
    </Box>
  );
}

function FontTile({ f, active, onClick, sample, label }: { f: UfFont; active: boolean; onClick: () => void; sample: string; label: string }) {
  return (
    <UnstyledButton onClick={onClick} className="uf-theme uf-font-tile" data-active={active || undefined}>
      <Text size="lg" fw={600} truncate style={{ fontFamily: `${f.family}, var(--uf-font)` }}>{sample}</Text>
      <Group gap={6} mt={4} wrap="nowrap">
        {active ? <IconCheck size={14} /> : <Text span c="dimmed" size="sm">—</Text>}
        <Text size="sm" fw={active ? 650 : 400} truncate>{label}</Text>
      </Group>
    </UnstyledButton>
  );
}

export function ThemePicker({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const t = useT(L);
  const { prefs, setPrefs } = usePrefs();
  const current = prefs.theme ?? THEMES[0].id;
  const font = prefs.font ?? DEFAULT_UI_FONT;
  const num = prefs.numFont ?? DEFAULT_NUM_FONT;
  useEffect(() => {
    if (opened) loadAllFonts(); // the samples are drawn in each font
  }, [opened]);
  return (
    <Modal opened={opened} onClose={onClose} size="xl" title={<Text fw={700}>{t('title')}</Text>}>
      <Stack gap="lg">
        {(['classic', 'warm', 'bright', 'light'] as const).map((g) => (
          <Stack key={g} gap="xs">
            <Text size="xs" fw={700} c="dimmed" tt="uppercase" style={{ letterSpacing: '0.06em' }}>{t('themes')} · {t(g)}</Text>
            <SimpleGrid cols={{ base: 2, sm: 4 }} spacing="sm">
              {THEMES.filter((x) => x.group === g).map((x) => (
                <UnstyledButton key={x.id} onClick={() => setPrefs({ theme: x.id })} className="uf-theme" data-active={x.id === current || undefined}>
                  <Preview t={x} />
                  <Group gap={6} mt={6} wrap="nowrap">
                    {x.id === current ? <IconCheck size={14} /> : <Text span c="dimmed" size="sm">—</Text>}
                    <Text size="sm" fw={x.id === current ? 650 : 400} truncate>{x.name}</Text>
                  </Group>
                </UnstyledButton>
              ))}
            </SimpleGrid>
          </Stack>
        ))}

        <Stack gap="xs">
          <Text size="xs" fw={700} c="dimmed" tt="uppercase" style={{ letterSpacing: '0.06em' }}>{t('font')}</Text>
          <SimpleGrid cols={{ base: 2, sm: 4 }} spacing="sm">
            {UI_FONTS.map((f) => (
              <FontTile key={f.id} f={f} active={f.id === font} onClick={() => setPrefs({ font: f.id })} sample={t('sample')} label={f.name} />
            ))}
          </SimpleGrid>
        </Stack>

        <Stack gap="xs">
          <Text size="xs" fw={700} c="dimmed" tt="uppercase" style={{ letterSpacing: '0.06em' }}>{t('num')}</Text>
          <SimpleGrid cols={{ base: 2, sm: 5 }} spacing="sm">
            {NUM_FONTS.map((f) => (
              <FontTile key={f.id} f={f.id === 'ui' ? UI_FONTS.find((x) => x.id === font) ?? UI_FONTS[0] : f} active={f.id === num}
                onClick={() => setPrefs({ numFont: f.id })} sample="48.3% · 12.9" label={f.id === 'ui' ? t('same') : f.name} />
            ))}
          </SimpleGrid>
        </Stack>

        <Text size="xs" c="dimmed">{t('hint')}</Text>
      </Stack>
    </Modal>
  );
}
