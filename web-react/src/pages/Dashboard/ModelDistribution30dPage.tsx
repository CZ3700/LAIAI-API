import React, { useEffect, useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import useSettingsStore from '../../store/settings';
import request from '../../utils/request';
import type { ModelTrend30dResponse } from '../../types';
import ModelDistribution30d from './ModelDistribution30d';

const ModelDistribution30dPage: React.FC = () => {
  const navigate = useNavigate();
  const location = useLocation();
  const { settings } = useSettingsStore();
  const currencySymbol = settings?.currency?.currency_symbol || '$';
  const [data, setData] = useState<ModelTrend30dResponse | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    request.get<ModelTrend30dResponse>('/dashboard/models_30d')
      .then((res) => {
        if (!cancelled) setData(res as unknown as ModelTrend30dResponse);
      })
      .catch((e) => {
        console.error(e);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const firstSegment = location.pathname.split('/')[1] || '';
  const backTo = firstSegment === 'dashboard' ? '/dashboard' : `/${firstSegment}/dashboard`;

  return (
    <ModelDistribution30d
      data={data}
      loading={loading}
      currencySymbol={currencySymbol}
      onBack={() => navigate(backTo)}
    />
  );
};

export default ModelDistribution30dPage;
