#include "esp_log.h"
#include "esp_err.h"

#include "esp_adc/adc_oneshot.h"

#include "cJSON.h"

#include "mqtt_manager.h"

#define VOLTAGE_ADC_UNIT      ADC_UNIT_1
#define VOLTAGE_ADC_CHANNEL  ADC_CHANNEL_4   // GPIO32

#define MAX_ADC_VALUE        4095
#define VOLT_PER_ADC_COUNT   (3.0f / MAX_ADC_VALUE)

static const char *TAG = "publish_info";

adc_oneshot_unit_handle_t adc_handle;


static float raw_to_volt(int raw)
{
    return VOLT_PER_ADC_COUNT * (float)raw;
}

void voltage_init(void)
{
    const adc_oneshot_unit_init_cfg_t unit_config = {
        .unit_id = VOLTAGE_ADC_UNIT,
    };

    ESP_ERROR_CHECK(
        adc_oneshot_new_unit(&unit_config, &adc_handle)
    );

    const adc_oneshot_chan_cfg_t channel_config = {
        .atten = ADC_ATTEN_DB_12,
        .bitwidth = ADC_BITWIDTH_DEFAULT,
    };

    ESP_ERROR_CHECK(
        adc_oneshot_config_channel(
            adc_handle,
            VOLTAGE_ADC_CHANNEL,
            &channel_config
        )
    );

    ESP_LOGI(TAG, "TEMT6000 initialized");
}

void publish_info(
    char *device_id,
    char *version,
    char *topic
)
{
    int raw = 0;

    esp_err_t err = adc_oneshot_read(
        adc_handle,
        VOLTAGE_ADC_CHANNEL,
        &raw
    );

    if (err != ESP_OK) {
        ESP_LOGE(
            TAG,
            "Voltage ADC read failed: %s",
            esp_err_to_name(err)
        );

        return;
    }

    float voltage = raw_to_volt(raw);

    cJSON *root = cJSON_CreateObject();

    if (root == NULL) {
        ESP_LOGE(TAG, "Failed to create JSON");
        return;
    }

    cJSON_AddStringToObject(
        root,
        "device_id",
        device_id
    );

    cJSON_AddNumberToObject(
        root,
        "voltage",
        voltage
    );

    cJSON_AddNumberToObject(
        root,
        "raw_voltage",
        raw
    );

    cJSON_AddStringToObject(
        root,
        "version",
        version
    );
    

    char *payload = cJSON_PrintUnformatted(root);

    if (payload == NULL) {
        cJSON_Delete(root);
        return;
    }

    int msg_id = mqtt_manager_publish(
        topic,
        payload,
        1,
        0
    );

    if (msg_id < 0) {
        ESP_LOGE(TAG, "Failed to publish info");
    }

    cJSON_free(payload);
    cJSON_Delete(root);
}