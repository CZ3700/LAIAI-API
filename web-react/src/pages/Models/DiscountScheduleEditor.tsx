import React, { useEffect, useMemo, useState } from 'react';
import { Button, Col, DatePicker, Form, InputNumber, Radio, Row, Switch, Tag, TimePicker, Typography } from 'antd';
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import dayjs from 'dayjs';
import utc from 'dayjs/plugin/utc';
import timezone from 'dayjs/plugin/timezone';
import { formatWallClock } from '../../utils/timedisplay';
import TimeMultiplierChart from './TimeMultiplierChart';
import { convertScheduleSlots, convertWallDateTime, scheduleInputTzName, type ScheduleTzMode } from './scheduleTzConvert';
import { discountChartItems, getSeedance2Resolutions, type DiscountSlotForm } from './discountSchedule';
import {
  ALL_WEEKDAYS,
  formatRate,
  formatWeekdayRange,
  isAllDayMultiplier,
  isAllWeekdays,
  isFreeTimeMultiplier,
  normalizeWeekdays,
  resolveTimeMultiplierAt,
} from '../../utils/timeMultipliers';

dayjs.extend(utc);
dayjs.extend(timezone);

const { Text } = Typography;

const WEEKDAY_CHIPS = [
  { v: 1, key: 'mon', l: '一' },
  { v: 2, key: 'tue', l: '二' },
  { v: 3, key: 'wed', l: '三' },
  { v: 4, key: 'thu', l: '四' },
  { v: 5, key: 'fri', l: '五' },
  { v: 6, key: 'sat', l: '六' },
  { v: 7, key: 'sun', l: '日' },
];

const WeekdayPicker: React.FC<{
  value?: number[];
  onChange?: (v: number[]) => void;
}> = ({ value, onChange }) => {
  const { t } = useTranslation();
  const selected = normalizeWeekdays(value);
  const toggle = (day: number) => {
    const next = selected.includes(day)
      ? selected.filter((d) => d !== day)
      : [...selected, day].sort((a, b) => a - b);
    if (next.length === 0) return;
    onChange?.(next);
  };
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
      {WEEKDAY_CHIPS.map((chip) => {
        const checked = selected.includes(chip.v);
        return (
          <Button
            key={chip.v}
            size="small"
            type={checked ? 'primary' : 'default'}
            onClick={() => toggle(chip.v)}
            style={{ minWidth: 28, padding: '0 6px' }}
          >
            {t(`models.weekdays.${chip.key}`, chip.l)}
          </Button>
        );
      })}
      <Button type="link" size="small" style={{ padding: '0 4px' }} onClick={() => onChange?.([...ALL_WEEKDAYS])}>{t('models.everyday', '每天')}</Button>
      <Button type="link" size="small" style={{ padding: '0 4px' }} onClick={() => onChange?.([1, 2, 3, 4, 5])}>{t('models.weekdays_btn', '工作日')}</Button>
      <Button type="link" size="small" style={{ padding: '0 4px' }} onClick={() => onChange?.([6, 7])}>{t('models.weekend_btn', '周末')}</Button>
    </div>
  );
};

function currentClock(defaultTz: string) {
  const now = dayjs();
  try {
    if (defaultTz.startsWith('UTC') || /^[+-]\d/.test(defaultTz)) {
      return now.utcOffset(defaultTz.replace('UTC', ''));
    }
    if (defaultTz) return now.tz(defaultTz);
  } catch {
    return now;
  }
  return now;
}

/** 秒级时钟文字：独立组件自刷新，不牵动父级编辑器重绘 */
const LiveClock: React.FC<{ tz: string }> = ({ tz }) => {
  const [, setTick] = useState(0);
  useEffect(() => {
    const timer = setInterval(() => setTick((n) => n + 1), 1000);
    return () => clearInterval(timer);
  }, []);
  return <>{formatWallClock(tz)}</>;
};

const DiscountRateInput: React.FC<{
  value?: number | null;
  onChange?: (value: number | null) => void;
  addonBefore?: React.ReactNode;
  placeholder?: string;
  min?: number;
}> = ({ value, onChange, addonBefore, placeholder, min = 0 }) => {
  const { t } = useTranslation();
  const freeLabel = t('common.free', '免费');
  const [editing, setEditing] = useState(false);
  return (
    <InputNumber
      value={value ?? undefined}
      placeholder={placeholder}
      onChange={(next) => {
        if (next == null || (next as unknown) === '') {
          onChange?.(null);
          return;
        }
        const n = Number(next);
        onChange?.(Number.isFinite(n) ? n : null);
      }}
      min={min}
      precision={3}
      step={0.1}
      style={{ width: '100%' }}
      addonBefore={addonBefore}
      onFocus={() => setEditing(true)}
      onBlur={() => setEditing(false)}
      formatter={(raw, info) => {
        if (info?.userTyping) return info.input;
        if (raw == null || (raw as unknown) === '') return '';
        const n = Number(raw);
        if (isFreeTimeMultiplier(n)) return editing ? '0' : freeLabel;
        return String(raw);
      }}
      parser={(raw) => {
        const str = String(raw ?? '');
        if (str.includes('免费') || str.includes('免費') || str.toLowerCase() === 'free') return 0;
        const text = str.trim();
        if (text === '' || text === '.') return '' as unknown as number;
        const n = Number(text);
        return Number.isFinite(n) ? n : ('' as unknown as number);
      }}
    />
  );
};

const DiscountScheduleEditor: React.FC<{
  isLight: boolean;
  operationTz: string;
  adminTz: string;
  allBillingRules?: any[];
}> = ({ isLight, operationTz, adminTz, allBillingRules = [] }) => {
  const { t } = useTranslation();
  const discountRateText = (rate: number): string => (isFreeTimeMultiplier(rate) ? t('common.free', '免费') : formatRate(rate));
  const form = Form.useFormInstance();
  const enabled = Form.useWatch('discount_schedule_enabled', form);
  const limitedEnabled = !!Form.useWatch('limited_discount_enabled', form);
  const limitedHasEnd = !!Form.useWatch('limited_discount_has_end', form);
  const limitedEndAt = Form.useWatch('limited_discount_end_at', form);
  const scheduleHasEnd = !!Form.useWatch('discount_schedule_has_end', form);
  const scheduleEndAt = Form.useWatch('discount_schedule_end_at', form);
  const billingRuleId = Form.useWatch('billing_rule_id', form);
  const siteOn = Form.useWatch('site_discount_enabled', form);
  const globalOn = Form.useWatch('global_discount_enabled', form);
  const inputTzMode = (Form.useWatch('discount_schedule_input_tz', form) || 'operation') as ScheduleTzMode;
  const limitedTzMode = (Form.useWatch('limited_discount_input_tz', form) || 'operation') as ScheduleTzMode;
  const rawSiteDiscount = Form.useWatch('site_discount', form);
  const siteFallback = rawSiteDiscount != null && rawSiteDiscount !== '' && Number.isFinite(Number(rawSiteDiscount)) && Number(rawSiteDiscount) > 0
    ? Number(rawSiteDiscount)
    : 1;
  const rawGlobalDiscount = Form.useWatch('global_discount', form);
  const globalFallback = rawGlobalDiscount != null && rawGlobalDiscount !== '' && Number.isFinite(Number(rawGlobalDiscount)) && Number(rawGlobalDiscount) > 0
    ? Number(rawGlobalDiscount)
    : 1;
  const slots = (Form.useWatch('discount_slots', form) || []) as DiscountSlotForm[];
  const clockTz = scheduleInputTzName(inputTzMode, operationTz, adminTz);
  const limitedTz = scheduleInputTzName(limitedTzMode, operationTz, adminTz);
  const [, setTick] = useState(0);
  const now = currentClock(clockTz);

  const selectedBr = useMemo(
    () => allBillingRules.find((b: any) => b.id === billingRuleId),
    [allBillingRules, billingRuleId],
  );
  const isSeedance2 = selectedBr?.billing_rule === 'seedance2.0';

  const sd2Resolutions = useMemo(
    () => getSeedance2Resolutions(selectedBr),
    [selectedBr],
  );

  useEffect(() => {
    // 时段命中/到期状态按 30 秒刷新即可；秒级时钟文字由 LiveClock 自行刷新，避免整块编辑器（含图表与时段表单）每秒重绘
    if (!globalOn) return;
    const timer = setInterval(() => setTick((n) => n + 1), 30_000);
    return () => clearInterval(timer);
  }, [globalOn]);

  const limNow = currentClock(limitedTz);
  const limitedEndStr = limitedHasEnd && limitedEndAt && typeof limitedEndAt.format === 'function'
    ? limitedEndAt.format('YYYY-MM-DD HH:mm:ss')
    : (limitedHasEnd && typeof limitedEndAt === 'string' ? limitedEndAt.trim() : '');
  const isLimitedExpired = Boolean(
    limitedEnabled
    && limitedHasEnd
    && limitedEndStr
    && limNow.format('YYYY-MM-DD HH:mm:ss') >= limitedEndStr,
  );

  const weekday = now.day() === 0 ? 7 : now.day();
  const minutes = now.hour() * 60 + now.minute();
  const scheduleEndStr = scheduleHasEnd && scheduleEndAt && typeof scheduleEndAt.format === 'function'
    ? scheduleEndAt.format('YYYY-MM-DD HH:mm:ss')
    : (scheduleHasEnd && typeof scheduleEndAt === 'string' ? scheduleEndAt.trim() : '');
  const isScheduleExpired = Boolean(scheduleEndStr && now.format('YYYY-MM-DD HH:mm:ss') >= scheduleEndStr);
  const scheduleActive = !limitedEnabled && !!enabled;
  const scheduleLive = scheduleActive && !isScheduleExpired;
  const siteNow = scheduleLive && siteOn
    ? resolveTimeMultiplierAt(discountChartItems(slots, 'site_discount', siteFallback), false, weekday, minutes)
    : siteFallback;
  const globalNow = scheduleLive && globalOn
    ? resolveTimeMultiplierAt(discountChartItems(slots, 'global_discount', globalFallback), false, weekday, minutes)
    : globalFallback;

  return (
    <div style={{ display: globalOn ? 'block' : 'none' }}>
      {/* 高级设置 · 限时折扣 */}
      <div style={{
        marginBottom: 12,
        padding: 12,
        borderRadius: 8,
        border: isLight ? '1px solid #f0f0f0' : '1px solid rgba(255,255,255,0.08)',
        background: isLight ? 'rgba(114,46,209,0.03)' : 'rgba(114,46,209,0.07)',
      }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, marginBottom: limitedEnabled ? 10 : 0 }}>
          <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
              <Text strong>{t('models.limited_discount_title', '高级设置 · 限时折扣')}</Text>
              <Tag color={isSeedance2 ? 'purple' : 'blue'} style={{ margin: 0, fontSize: 10.5, lineHeight: '16px', paddingInline: 5 }}>
                {isSeedance2 ? t('models.limited_seedance2_tag', 'Seedance 2 按分辨率') : t('models.limited_general_tag', '通用单折扣')}
              </Tag>
              {limitedEnabled && (
                <Tag color={isLimitedExpired ? 'default' : 'success'} style={{ margin: 0, fontSize: 10.5, lineHeight: '16px', paddingInline: 5 }}>
                  {isLimitedExpired
                    ? t('models.discount_expired_restored', '已到期 · 已恢复全站折扣倍率')
                    : (limitedHasEnd && limitedEndStr
                        ? t('models.discount_active_until', { end: limitedEndStr, defaultValue: `生效中 · 截止 ${limitedEndStr}` })
                        : t('models.discount_long_term_active', '长期生效中'))}
                </Tag>
              )}
            </div>
            <Text type="secondary" style={{ fontSize: 12, display: 'block', marginTop: 4 }}>
              {isSeedance2
                ? t('models.limited_seedance2_desc', '按分辨率设置活动折扣；可设结束时间，到期自动恢复上方全站折扣倍率')
                : t('models.limited_general_desc', '设置活动折扣；可设结束时间，到期自动恢复上方全站折扣倍率')}
            </Text>
          </div>
          <Form.Item name="limited_discount_enabled" valuePropName="checked" initialValue={false} style={{ margin: 0 }}>
            <Switch
              size="small"
              checkedChildren={t('common.on', '开启')}
              unCheckedChildren={t('common.off', '关闭')}
              onChange={(checked) => {
                if (checked) {
                  form.setFieldValue('discount_schedule_enabled', false);
                }
              }}
            />
          </Form.Item>
        </div>

        <div style={{ display: limitedEnabled ? 'block' : 'none' }}>
          <Row gutter={[10, 10]}>
            <Col xs={24} sm={12} style={{ display: siteOn ? 'block' : 'none' }}>
              <Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                {t('models.promo_floor_label', { fallback: discountRateText(siteFallback), defaultValue: `活动限价（0 为免费，留空沿用 ${discountRateText(siteFallback)}）` })}
              </Text>
              <Form.Item name="limited_site_discount" style={{ marginBottom: 0 }}>
                <DiscountRateInput addonBefore={t('models.floor_short', '限价')} placeholder={t('models.default_rate_ph', { rate: discountRateText(siteFallback), defaultValue: `默认 ${discountRateText(siteFallback)}` })} />
              </Form.Item>
            </Col>
            {!isSeedance2 && (
              <Col xs={24} sm={12}>
                <Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                  {t('models.promo_discount_label', '活动折扣（0 为免费）')}
                </Text>
                <Form.Item name="limited_discount_rate" style={{ marginBottom: 0 }}>
                  <DiscountRateInput addonBefore={t('models.discount_short', '折扣')} placeholder={t('models.enter_promo_discount', '输入活动折扣')} />
                </Form.Item>
              </Col>
            )}
          </Row>

          {isSeedance2 && (
            <div style={{ marginTop: 10 }}>
              <Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                {t('models.promo_res_discount_label', { fallback: discountRateText(globalFallback), defaultValue: `各分辨率活动折扣（0 为免费，留空沿用全站折扣倍率 ${discountRateText(globalFallback)}）` })}
              </Text>
              <Row gutter={[10, 8]}>
                {sd2Resolutions.map((resKey) => (
                  <Col xs={24} sm={12} key={resKey}>
                    <Form.Item
                      name={['limited_resolution_discounts', resKey]}
                      style={{ marginBottom: 0 }}
                    >
                      <DiscountRateInput
                        addonBefore={resKey}
                        placeholder={t('models.default_rate_ph', { rate: discountRateText(globalFallback), defaultValue: `默认 ${discountRateText(globalFallback)}` })}
                      />
                    </Form.Item>
                  </Col>
                ))}
              </Row>
            </div>
          )}

          {siteOn && (
            <Text type="secondary" style={{ fontSize: 11.5, display: 'block', marginTop: 6 }}>
              {t('models.promo_below_floor_hint', '活动折扣低于限价时自动放行。')}
            </Text>
          )}

          <div
            style={{
              marginTop: 10,
              paddingTop: 10,
              borderTop: isLight ? '1px dashed rgba(0,0,0,0.08)' : '1px dashed rgba(255,255,255,0.1)',
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8, marginBottom: limitedHasEnd ? 8 : 4 }}>
              <Text style={{ fontSize: 12 }}>{t('models.discount_validity', '折扣有效期')}</Text>
              <Form.Item name="limited_discount_has_end" initialValue={false} style={{ margin: 0 }}>
                <Radio.Group size="small" buttonStyle="solid">
                  <Radio.Button value={false}>{t('models.long_term', '长期有效')}</Radio.Button>
                  <Radio.Button value={true}>{t('models.set_end_time', '设置结束时间')}</Radio.Button>
                </Radio.Group>
              </Form.Item>
            </div>
            <div style={{ display: limitedHasEnd ? 'block' : 'none' }}>
              <Form.Item name="limited_discount_input_tz" initialValue="operation" style={{ marginBottom: 8 }}>
                <Radio.Group
                  buttonStyle="solid"
                  size="small"
                  onChange={(e) => {
                    const next = e.target.value as ScheduleTzMode;
                    if (!limitedEndStr) return;
                    const converted = convertWallDateTime(
                      limitedEndStr,
                      limitedTz,
                      scheduleInputTzName(next, operationTz, adminTz),
                    );
                    form.setFieldValue('limited_discount_end_at', dayjs(converted));
                  }}
                >
                  <Radio.Button value="operation">{t('models.operation_tz', '运营时区')}</Radio.Button>
                  <Radio.Button value="admin_display">{t('models.admin_tz', '后台时区')}</Radio.Button>
                </Radio.Group>
              </Form.Item>
              <Form.Item name="limited_discount_end_at" style={{ marginBottom: 4 }}>
                <DatePicker
                  showTime
                  format="YYYY-MM-DD HH:mm:ss"
                  placeholder={t('models.select_promo_end_time', '选择活动结束时间')}
                  style={{ width: '100%' }}
                  needConfirm={false}
                />
              </Form.Item>
              <Text type="secondary" style={{ fontSize: 11.5, display: 'block' }}>
                {t('models.tz_end_hint_prefix', {
                  tzMode: limitedTzMode === 'admin_display' ? t('models.admin_tz', '后台时区') : t('models.operation_tz', '运营时区'),
                  tz: limitedTz,
                  defaultValue: `按${limitedTzMode === 'admin_display' ? '后台时区' : '运营时区'} (${limitedTz}，`,
                })}
                <LiveClock tz={limitedTz} />
                {t('models.tz_end_hint_suffix', {
                  opTz: operationTz,
                  rate: discountRateText(globalFallback),
                  defaultValue: `) 填写，保存后按运营时区 (${operationTz}) 判定；到期恢复全站折扣倍率 (${discountRateText(globalFallback)})。`,
                })}
              </Text>
            </div>
            {!limitedHasEnd && (
              <Text type="secondary" style={{ fontSize: 11.5, display: 'block' }}>
                {t('models.no_end_time_hint', { rate: discountRateText(globalFallback), defaultValue: `不设结束时间即长期有效；关闭开关即恢复全站折扣倍率 (${discountRateText(globalFallback)})。` })}
              </Text>
            )}
          </div>
        </div>
      </div>

      {/* 高级设置 · 按天规划时段折扣 */}
      <div style={{
        marginBottom: 16,
        padding: 12,
        borderRadius: 8,
        border: isLight ? '1px solid #f0f0f0' : '1px solid rgba(255,255,255,0.08)',
        background: isLight ? 'rgba(22,119,255,0.03)' : 'rgba(22,119,255,0.06)',
        opacity: limitedEnabled ? 0.6 : 1,
      }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, marginBottom: limitedEnabled ? 0 : 8 }}>
          <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
              <Text strong>{t('models.schedule_discount_title', '高级设置 · 按天规划时段折扣')}</Text>
              {scheduleActive && (
                <Tag color={isScheduleExpired ? 'default' : 'success'} style={{ margin: 0, fontSize: 10.5, lineHeight: '16px', paddingInline: 5 }}>
                  {isScheduleExpired
                    ? t('models.discount_expired_restored', '已到期 · 已恢复全站折扣倍率')
                    : (scheduleEndStr
                        ? t('models.discount_active_until', { end: scheduleEndStr, defaultValue: `生效中 · 截止 ${scheduleEndStr}` })
                        : t('models.discount_long_term_active', '长期生效中'))}
                </Tag>
              )}
            </div>
            {limitedEnabled ? (
              <Text type="warning" style={{ fontSize: 12, display: 'block', marginTop: 4 }}>
                {t('models.schedule_disabled_by_limited', '已开启「高级设置 · 限时折扣」，按天规划时段折扣不可同时使用')}
              </Text>
            ) : (
              <Text type="secondary" style={{ fontSize: 12, display: 'block', marginTop: 4 }}>
                {inputTzMode === 'admin_display' ? t('models.admin_time_label', '后台时间') : t('models.operation_time_label', '运营时间')}: <LiveClock tz={clockTz} /> · {clockTz}
                {scheduleLive && siteOn ? ` · ${t('models.site_discount_floor', '折扣限价')} ${discountRateText(siteNow)}` : ''}
                {scheduleLive && globalOn ? ` · ${t('models.global_discount', '全站折扣')} ${discountRateText(globalNow)}` : ''}
              </Text>
            )}
          </div>
          <Form.Item name="discount_schedule_enabled" valuePropName="checked" initialValue={false} style={{ margin: 0 }}>
            <Switch size="small" disabled={limitedEnabled} checkedChildren={t('common.on', '开启')} unCheckedChildren={t('common.off', '关闭')} />
          </Form.Item>
        </div>
        {!limitedEnabled && (
          <>
            <Form.Item name="discount_schedule_input_tz" initialValue="operation" style={{ marginBottom: 8 }}>
              <Radio.Group
                buttonStyle="solid"
                size="small"
                onChange={(e) => {
                  const next = e.target.value as ScheduleTzMode;
                  const fromTz = scheduleInputTzName(inputTzMode, operationTz, adminTz);
                  const toTz = scheduleInputTzName(next, operationTz, adminTz);
                  form.setFieldsValue({
                    discount_slots: convertScheduleSlots(slots, fromTz, toTz),
                    ...(scheduleEndStr ? { discount_schedule_end_at: dayjs(convertWallDateTime(scheduleEndStr, fromTz, toTz)) } : {}),
                  });
                }}
              >
                <Radio.Button value="operation">{t('models.operation_tz', '运营时区')}</Radio.Button>
                <Radio.Button value="admin_display">{t('models.admin_tz', '后台时区')}</Radio.Button>
              </Radio.Group>
            </Form.Item>
            <Text type="secondary" style={{ fontSize: 12, display: 'block' }}>
              {t('models.schedule_tz_hint', '按所选时区填时段，保存后按运营时区计费；未命中用上方统一折扣。填 0 为免费，全站免费时限价也须为 0。')}
            </Text>
          </>
        )}
        <div style={{ display: scheduleActive ? 'block' : 'none', marginTop: 12 }}>
        <Form.List name="discount_slots" initialValue={[]}>
          {(fields, { add, remove }) => (
            <>
              {fields.map(({ key, name: listName, ...restField }) => {
                const row = slots[listName] || {};
                const allDay = isAllDayMultiplier(row);
                const daysLabel = isAllWeekdays(row.days)
                  ? t('models.everyday', '每天')
                  : formatWeekdayRange(row.days, WEEKDAY_CHIPS.map((chip) => t(`models.weekdays.${chip.key}`, chip.l)), t('models.everyday', '每天'));
                return (
                  <div
                    key={key}
                    style={{
                      marginBottom: 12,
                      padding: '12px 12px 8px',
                      borderRadius: 8,
                      border: isLight ? '1px solid #f0f0f0' : '1px solid #303030',
                      background: isLight ? '#fff' : 'rgba(0,0,0,0.15)',
                    }}
                  >
                    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, marginBottom: 8 }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                        <Text style={{ fontSize: 13 }}>{allDay ? t('models.all_day', '全天') : t('models.time_slot', '时段')}</Text>
                        <Form.Item {...restField} name={[listName, 'all_day']} valuePropName="checked" style={{ margin: 0 }}>
                          <Switch size="small" checkedChildren={t('models.all_day', '全天')} unCheckedChildren={t('models.time_slot', '时段')} />
                        </Form.Item>
                      </div>
                      <Button type="text" danger icon={<DeleteOutlined />} onClick={() => remove(listName)} size="small" />
                    </div>
                    {!allDay && (
                      <Row gutter={12}>
                        <Col xs={24} sm={12}>
                          <Form.Item
                            {...restField}
                            name={[listName, 'start']}
                            rules={globalOn && scheduleActive && !allDay ? [{ required: true, message: t('models.select_start_time', '选择开始时间') }] : []}
                            style={{ marginBottom: 8 }}
                          >
                            <TimePicker placeholder={t('models.start_time', '开始时间')} format="HH:mm" style={{ width: '100%' }} allowClear={false} needConfirm={false} />
                          </Form.Item>
                        </Col>
                        <Col xs={24} sm={12}>
                          <Form.Item
                            {...restField}
                            name={[listName, 'end']}
                            rules={globalOn && scheduleActive && !allDay ? [{ required: true, message: t('models.select_end_time', '选择结束时间') }] : []}
                            style={{ marginBottom: 8 }}
                          >
                            <TimePicker placeholder={t('models.end_time', '结束时间')} format="HH:mm" style={{ width: '100%' }} allowClear={false} needConfirm={false} />
                          </Form.Item>
                        </Col>
                      </Row>
                    )}
                    <Row gutter={12}>
                      <Col xs={24} sm={12} style={{ display: siteOn ? 'block' : 'none' }}>
                        <Form.Item
                          {...restField}
                          name={[listName, 'site_discount']}
                          style={{ marginBottom: 8 }}
                          initialValue={siteFallback}
                        >
                          <DiscountRateInput addonBefore={t('models.floor_short', '限价')} placeholder={t('models.default_rate_ph', { rate: discountRateText(siteFallback), defaultValue: `默认 ${discountRateText(siteFallback)}` })} />
                        </Form.Item>
                      </Col>
                      <Col xs={24} sm={12} style={{ display: globalOn ? 'block' : 'none' }}>
                        <Form.Item
                          {...restField}
                          name={[listName, 'global_discount']}
                          style={{ marginBottom: 8 }}
                          initialValue={globalFallback}
                        >
                          <DiscountRateInput addonBefore={t('models.global_short', '全站')} placeholder={t('models.default_rate_ph', { rate: discountRateText(globalFallback), defaultValue: `默认 ${discountRateText(globalFallback)}` })} />
                        </Form.Item>
                      </Col>
                    </Row>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginBottom: 4 }}>
                      <Text type="secondary" style={{ fontSize: 12 }}>{t('models.weekday_label', '星期')}</Text>
                      <Form.Item {...restField} name={[listName, 'days']} initialValue={[...ALL_WEEKDAYS]} style={{ marginBottom: 0, flex: 1 }}>
                        <WeekdayPicker />
                      </Form.Item>
                    </div>
                    <Text type="secondary" style={{ fontSize: 12 }}>
                      {allDay
                        ? `${daysLabel} ${t('models.all_day', '全天')}`
                        : t('models.slot_days_desc', { days: daysLabel, defaultValue: `${daysLabel}：此时段用上面折扣，其余用统一折扣` })}
                    </Text>
                  </div>
                );
              })}
              <div style={{ display: 'flex', gap: 8 }}>
                <Button
                  type="dashed"
                  block
                  icon={<PlusOutlined />}
                  onClick={() => add({
                    all_day: false,
                    days: [...ALL_WEEKDAYS],
                    site_discount: siteFallback,
                    global_discount: globalFallback,
                  })}
                >
                  {t('models.add_time_slot', '添加时段')}
                </Button>
                <Button
                  type="dashed"
                  block
                  icon={<PlusOutlined />}
                  onClick={() => {
                    const used = new Set<number>();
                    slots.forEach((item) => {
                      if (isAllDayMultiplier(item)) normalizeWeekdays(item.days).forEach((day) => used.add(day));
                    });
                    const nextDay = [6, 7, 1, 2, 3, 4, 5].find((day) => !used.has(day)) || 7;
                    add({
                      all_day: true,
                      days: [nextDay],
                      site_discount: siteFallback,
                      global_discount: globalFallback,
                    });
                  }}
                >
                  {t('models.add_all_day_slot', '添加某天全天')}
                </Button>
              </div>
            </>
          )}
        </Form.List>
        {scheduleActive && slots.length > 0 && siteOn && (
          <TimeMultiplierChart
            items={discountChartItems(slots, 'site_discount', siteFallback)}
            invert={false}
            isLight={isLight}
            title={t('models.site_discount_floor', '折扣限价')}
            description={t('models.site_floor_chart_desc', '色块是该时段折扣限价，空白用上方统一限价。')}
          />
        )}
        {scheduleActive && slots.length > 0 && globalOn && (
          <TimeMultiplierChart
            items={discountChartItems(slots, 'global_discount', globalFallback)}
            invert={false}
            isLight={isLight}
            title={t('models.global_discount', '全站折扣')}
            description={t('models.global_chart_desc', '色块是该时段全站折扣，空白用上方统一折扣。')}
          />
        )}
        <div
          style={{
            marginTop: 10,
            paddingTop: 10,
            borderTop: isLight ? '1px dashed rgba(0,0,0,0.08)' : '1px dashed rgba(255,255,255,0.1)',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8, marginBottom: scheduleHasEnd ? 8 : 4 }}>
            <Text style={{ fontSize: 12 }}>{t('models.schedule_validity', '规划有效期')}</Text>
            <Form.Item name="discount_schedule_has_end" initialValue={false} style={{ margin: 0 }}>
              <Radio.Group size="small" buttonStyle="solid">
                <Radio.Button value={false}>{t('models.long_term', '长期有效')}</Radio.Button>
                <Radio.Button value={true}>{t('models.set_end_time', '设置结束时间')}</Radio.Button>
              </Radio.Group>
            </Form.Item>
          </div>
          <div style={{ display: scheduleHasEnd ? 'block' : 'none' }}>
            <Form.Item name="discount_schedule_end_at" style={{ marginBottom: 4 }}>
              <DatePicker
                showTime
                format="YYYY-MM-DD HH:mm:ss"
                placeholder={t('models.select_schedule_end_time', '选择规划结束时间')}
                style={{ width: '100%' }}
                needConfirm={false}
              />
            </Form.Item>
            <Text type="secondary" style={{ fontSize: 11.5, display: 'block' }}>
              {t('models.schedule_end_hint', { clockTz, opTz: operationTz, rate: discountRateText(globalFallback), defaultValue: `按上方所选时区 (${clockTz}) 填写，保存后按运营时区 (${operationTz}) 判定；到期恢复全站折扣倍率 (${discountRateText(globalFallback)})。` })}
            </Text>
          </div>
          {!scheduleHasEnd && (
            <Text type="secondary" style={{ fontSize: 11.5, display: 'block' }}>
              {t('models.no_end_time_hint', { rate: discountRateText(globalFallback), defaultValue: `不设结束时间即长期有效；关闭开关即恢复全站折扣倍率 (${discountRateText(globalFallback)})。` })}
            </Text>
          )}
        </div>
        </div>
      </div>
    </div>
  );
};

export default DiscountScheduleEditor;
