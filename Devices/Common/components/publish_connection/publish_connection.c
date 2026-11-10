#include "esp_log.h"
#include "cJSON.h"
#include "mqtt_manager.h"

static const char *TAG = "publish_connection";

void publish_connection(
    int connect,
    char *device_id,
    char *topic
)
{
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

    cJSON_AddStringToObject(
        root,
        "signal",
        connect ? "connect" : "disconnect"
    );

    char *payload = cJSON_PrintUnformatted(root);

    if (payload == NULL) {
        ESP_LOGE(TAG, "Failed to serialize JSON");

        cJSON_Delete(root);
        return;
    }

    int msg_id = mqtt_manager_publish(
        topic,
        payload,
        1,
        0
    );

    if (msg_id >= 0) {

        ESP_LOGI(
            TAG,
            "Published to %s: %s",
            topic,
            payload
        );

    } else {

        ESP_LOGE(
            TAG,
            "MQTT publish failed"
        );
    }

    cJSON_free(payload);
    cJSON_Delete(root);
}